package web

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
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
	rows := []tlRow{at(100), at(90), at(80), at(80), at(70), at(60)}

	// all sources gave everything they have: one page, no more
	pg := &tlPage{limit: 50}
	if page, next := pg.cut(rows, 50); len(page) != 6 || next != 0 {
		t.Errorf("all: %d rows, next %d", len(page), next)
	}
	// a page ends inside a second: the rest of that second comes along
	pg = &tlPage{limit: 50}
	if page, next := pg.cut(rows, 3); len(page) != 4 || next != 80 {
		t.Errorf("tie: %d rows, next %d", len(page), next)
	}
	// a source that gave its limit has more below 70: older rows are not complete, so they wait
	pg = &tlPage{limit: 4}
	pg.took(4, time.Unix(70, 0))
	if page, next := pg.cut(rows, 50); len(page) != 4 || next != 80 {
		t.Errorf("edge: %d rows, next %d", len(page), next)
	}
}
