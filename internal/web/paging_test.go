package web

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/feed"
)

// savePosts stores n posts of a followed feed, one every step seconds back from start, titled "<name> <i>".
// A post whose number is in same shares its second with the one before it.
func savePosts(t *testing.T, s *Server, name string, n int, start time.Time, step int, same map[int]bool) {
	t.Helper()
	u, _ := s.st.UserByName("alice")
	fu := "http://" + name + ".example/feed.xml"
	if err := s.space("alice").Follow(u.ID, fu); err != nil {
		t.Fatal(err)
	}
	xml := `<rss version="2.0"><channel><title>Feed ` + name + `</title>`
	at := start
	for i := 0; i < n; i++ {
		if !same[i] {
			at = at.Add(-time.Duration(step) * time.Second)
		}
		xml += fmt.Sprintf(`<item><title>Post %s %03d</title><link>http://%s.example/%d</link><guid>http://%s.example/%d</guid><pubDate>%s</pubDate><description>x</description></item>`,
			name, i, name, i, name, i, at.UTC().Format(time.RFC1123Z))
	}
	p, err := feed.Parse([]byte(xml+`</channel></rss>`), start)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveFeed(fu, p); err != nil {
		t.Fatal(err)
	}
}

var postTitle = regexp.MustCompile(`Post ([a-z]) (\d{3})`)
var olderLink = regexp.MustCompile(`<a href="([^"]+)" rel="next">Older posts</a>`)

// The timeline shows 50 posts at a time and "Older posts" leads on, so that following the links shows
// every post once, newest first, even where several posts share a second or two feeds interleave.
func TestTimelinePagesShowEveryPostOnce(t *testing.T) {
	s, _ := setup(t)
	start := time.Now().Add(-time.Hour)
	// feed a: a post every 60 s, with a run of six sharing a second where the first page ends
	savePosts(t, s, "a", 130, start, 60, map[int]bool{47: true, 48: true, 49: true, 50: true, 51: true, 52: true})
	// feed b: a post every 90 s, so it interleaves with a and the two run out at different times
	savePosts(t, s, "b", 90, start, 90, nil)
	if err := s.space("alice").SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	seen := map[string]int{}
	var order []string
	path, pages := "/timeline", 0
	for path != "" {
		if pages++; pages > 10 {
			t.Fatal("the pages do not end")
		}
		body := form(s, "GET", path, ck, nil, "").Body.String()
		got := postTitle.FindAllStringSubmatch(body, -1)
		if len(got) > 0 && len(got) < 50 && olderLink.MatchString(body) {
			t.Errorf("page %d has %d posts and still leads on", pages, len(got))
		}
		for _, m := range got {
			seen[m[1]+m[2]]++
			order = append(order, m[1]+m[2])
		}
		if pages > 1 && !strings.Contains(body, "Back to the newest") {
			t.Errorf("page %d has no way back", pages)
		}
		path = ""
		if m := olderLink.FindStringSubmatch(body); m != nil {
			path = html.UnescapeString(m[1])
			if strings.Contains(path, "%") || !regexp.MustCompile(`before=\d{4}-\d\d-\d\d_\d\d-\d\d$`).MatchString(path) {
				t.Errorf("the address is not a plain date: %s", path)
			}
		}
	}
	if pages != 5 { // 220 posts, 50 to a page
		t.Errorf("%d pages, want 5", pages)
	}
	if len(seen) != 220 {
		t.Errorf("%d different posts shown, want 220", len(seen))
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("post %s shown %d times", k, n)
		}
	}
	if len(order) > 0 && order[0] != "a000" && order[0] != "b000" {
		t.Errorf("first post is %s, want the newest", order[0])
	}
}

func TestTimelineCutKeepsOnlyWhatTheSourcesGaveWhole(t *testing.T) {
	at := func(sec int64) tlRow { return tlRow{when: time.Unix(sec, 0)} }
	// minutes 100, 90, 80 (two posts), 70, 60
	rows := []tlRow{at(6000), at(5400), at(4800), at(4810), at(4200), at(3600)}

	// all sources gave everything they have: one page, no more
	pg := &tlPage{limit: 50}
	if page, next := pg.cut(rows, 50); len(page) != 6 || next != 0 {
		t.Errorf("all: %d rows, next %d", len(page), next)
	}
	// a page ends inside a minute: the rest of that minute comes along, and the next page starts before it
	pg = &tlPage{limit: 50}
	if page, next := pg.cut(rows, 3); len(page) != 4 || next != 4800 {
		t.Errorf("tie: %d rows, next %d", len(page), next)
	}
	// a source that gave its limit has more below minute 70: that minute and older are not complete, so they wait
	pg = &tlPage{limit: 4}
	pg.took(4, time.Unix(4210, 0))
	if page, next := pg.cut(rows, 50); len(page) != 4 || next != 4800 {
		t.Errorf("edge: %d rows, next %d", len(page), next)
	}
}

func TestBeforeIsWrittenAsADate(t *testing.T) {
	want := time.Date(2026, 10, 27, 14, 35, 0, 0, time.UTC).Unix()
	for _, v := range []string{"2026-10-27_14-35", "2026-10-27:14:35", "2026-10-27T14:35", "2026-10-27 14:35", " 2026-10-27:14:35 ", strconv.FormatInt(want, 10)} {
		if got := parseBefore(v); got != want {
			t.Errorf("%q: %d, want %d", v, got, want)
		}
	}
	for _, v := range []string{"", "yesterday", "2026-13-01:00:00"} {
		if got := parseBefore(v); got != 0 {
			t.Errorf("%q: %d, want 0", v, got)
		}
	}
}
