// Command hn-import copies the current Hacker News front page into a new Weft database:
// every story and comment becomes a post in its author's own feed, every comment a reply to its
// parent, and every author a user. It exists to give the UI a large, real conversation to run on.
//
//	hn-import -db hackernews.db -port 8090
//	weft serve -db hackernews.db -addr :8090 -dev -poll 30s
//
// Hacker News does not publish who voted or what comments scored, so the only votes are each
// story's point total, stored as anonymous trusted likes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/md"
	"github.com/hbroek/weft/internal/store"
)

type node struct {
	ID        int64   `json:"id"`
	Author    string  `json:"author"`
	CreatedAt int64   `json:"created_at_i"`
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Text      string  `json:"text"`
	Points    *int    `json:"points"`
	Type      string  `json:"type"`
	Children  []*node `json:"children"`
}

type importer struct {
	st                   *store.Instance
	scheme, domain, port string
	users                map[string]*user // by HN name
	taken                map[string]bool  // weft names in use
	subs                 map[subKey][]store.Subreply
	stats                struct{ users, posts, replies, votes, deleted, maxDepth int }
	storyFeeds           map[string][]feed.ParsedItem // feed URL -> its story items, to prefill followers' caches
	storyTitles          map[string]string
}

type user struct {
	id            int64
	name, display string
	sp            *store.Space
}

type subKey struct {
	sp   *store.Space // the space of the post that caches the subtree
	post int64
	via  string
}

// posted is a post as the importer knows it after writing it.
type posted struct {
	id        int64
	url, feed string
	sp        *store.Space
}

var client = &http.Client{Timeout: 30 * time.Second}

func getJSON(u string, v any) error {
	resp, err := client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func main() {
	dbPath := flag.String("db", "hackernews.db", "new SQLite file to create")
	n := flag.Int("n", 20, "how many front-page stories to copy")
	scheme := flag.String("scheme", "http", "scheme of the instance that will serve this database")
	domain := flag.String("domain", "localhost", "base domain of that instance")
	port := flag.String("port", "8090", "port of that instance (post URLs contain it)")
	viewer := flag.String("viewer", "me", "name of the user created to browse the data, who follows every story's submitter")
	password := flag.String("password", "hackernews", "the viewer's login password")
	flag.Parse()
	if _, err := os.Stat(*dbPath); err == nil {
		log.Fatalf("%s already exists; delete it first (this tool only creates new databases)", *dbPath)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	im := &importer{st: st, scheme: *scheme, domain: *domain, port: *port, users: map[string]*user{}, taken: map[string]bool{},
		subs: map[subKey][]store.Subreply{}, storyFeeds: map[string][]feed.ParsedItem{}, storyTitles: map[string]string{}}

	var ids []int64
	if err := getJSON("https://hacker-news.firebaseio.com/v0/topstories.json", &ids); err != nil {
		log.Fatal(err)
	}
	if len(ids) > *n {
		ids = ids[:*n]
	}
	var trees []*node
	for i, id := range ids {
		var t node
		if err := getJSON(fmt.Sprintf("https://hn.algolia.com/api/v1/items/%d", id), &t); err != nil {
			log.Fatal(err)
		}
		trees = append(trees, &t)
		log.Printf("fetched %2d/%d  %-60.60s  %d comments", i+1, len(ids), t.Title, count(&t)-1)
	}

	for _, t := range trees {
		root := im.story(t)
		for _, c := range t.Children {
			im.comment(c, root, nil, root, 1)
		}
	}
	// the second level each post caches: replies to its approved replies
	for k, rows := range im.subs {
		if len(rows) > 50 {
			rows = rows[:50]
		}
		if _, err := k.sp.ReplaceSubreplies(k.post, k.via, rows); err != nil {
			log.Fatal(err)
		}
	}

	// a user to browse as, who follows the submitters, with their stories already in the timeline
	if _, err := st.AddUser(*viewer, "Viewer", "Browses the copy of Hacker News."); err != nil {
		log.Fatal(err)
	}
	vsp, _, err := st.Space(*viewer)
	if err != nil {
		log.Fatal(err)
	}
	if err := vsp.SetPassword(*password); err != nil {
		log.Fatal(err)
	}
	v, _ := vsp.User()
	var feeds []string
	for f := range im.storyFeeds {
		feeds = append(feeds, f)
	}
	sort.Strings(feeds)
	for _, f := range feeds {
		if err := st.SaveFeed(f, &feed.Parsed{Title: im.storyTitles[f], Link: strings.TrimSuffix(f, "feed.xml"), Items: im.storyFeeds[f]}); err != nil {
			log.Fatal(err)
		}
		if err := vsp.Follow(v.ID, f); err != nil {
			log.Fatal(err)
		}
	}
	s := im.stats
	fmt.Printf("\n%s: %d users, %d stories and comments (%d replies, deepest %d levels), %d votes, %d deleted comments kept as placeholders\n",
		*dbPath, s.users, s.posts, s.replies, s.maxDepth, s.votes, s.deleted)
	fmt.Printf("\nServe it:   make run-hn      (or: ./bin/weft serve -db %s -addr :%s -dev -poll 30s)\n", *dbPath, *port)
	fmt.Printf("Browse as:  %s://%s.%s:%s/login   password %q\n", *scheme, *viewer, *domain, *port, *password)
}

func count(n *node) int {
	c := 1
	for _, ch := range n.Children {
		c += count(ch)
	}
	return c
}

var nameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// user returns the Weft user for a Hacker News name, creating it. Names are lowercased, with
// underscores and other characters turned into hyphens so that name.localhost is a valid host.
func (im *importer) user(hn string) *user {
	if hn == "" {
		hn = "hn-deleted"
	}
	if u := im.users[hn]; u != nil {
		return u
	}
	name := strings.Trim(nameChars.ReplaceAllString(strings.ToLower(hn), "-"), "-")
	if name == "" || name[0] == '-' {
		name = "u" + name
	}
	if len(name) > 28 {
		name = name[:28]
	}
	base := name
	for i := 2; im.taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	im.taken[name] = true
	if _, err := im.st.AddUser(name, hn, "Hacker News user "+hn+"."); err != nil {
		log.Fatalf("user %q: %v", name, err)
	}
	sp, _, err := im.st.Space(name)
	if err != nil {
		log.Fatal(err)
	}
	row, err := sp.User()
	if err != nil {
		log.Fatal(err)
	}
	u := &user{id: row.ID, name: name, display: hn, sp: sp}
	im.users[hn] = u
	im.stats.users++
	return u
}

func (im *importer) host(u *user) string {
	return fmt.Sprintf("%s://%s.%s:%s", im.scheme, u.name, im.domain, im.port)
}

func (im *importer) write(u *user, n *node, title, markdown string, parent, root *posted) *posted {
	htmlOut, err := md.Render(markdown)
	if err != nil {
		htmlOut = "<p>" + html.EscapeString(markdown) + "</p>"
	}
	np := store.NewPost{UserID: u.id, Title: title, Markdown: markdown, HTML: htmlOut, Created: time.Unix(n.CreatedAt, 0)}
	if parent != nil {
		np.InReplyTo, np.InReplyToFeed, np.Root, np.RootFeed = parent.url, parent.feed, root.url, root.feed
	}
	p, err := u.sp.AddPost(np)
	if err != nil {
		log.Fatalf("post %d: %v", n.ID, err)
	}
	im.stats.posts++
	return &posted{id: p.ID, url: im.host(u) + "/posts/" + p.Slug, feed: im.host(u) + "/feed.xml", sp: u.sp}
}

func (im *importer) story(n *node) *posted {
	u := im.user(n.Author)
	var sb strings.Builder
	if n.URL != "" {
		host := n.URL
		if pu, err := url.Parse(n.URL); err == nil {
			host = strings.TrimPrefix(pu.Host, "www.")
		}
		fmt.Fprintf(&sb, "[%s](%s)\n\n", host, n.URL)
	}
	if n.Text != "" {
		sb.WriteString(toMarkdown(n.Text) + "\n\n")
	}
	fmt.Fprintf(&sb, "Discussed on [Hacker News](https://news.ycombinator.com/item?id=%d).", n.ID)
	root := im.write(u, n, n.Title, sb.String(), nil, nil)

	votes := 0
	if n.Points != nil {
		votes = *n.Points
	}
	for i := 0; i < votes; i++ {
		err := u.sp.UpsertLikeIn(store.LikeIn{PostID: root.id, Source: fmt.Sprintf("hn-vote:%d:%d", n.ID, i), Author: "Hacker News voter",
			Trusted: true})
		if err != nil {
			log.Fatal(err)
		}
	}
	im.stats.votes += votes
	im.storyFeeds[root.feed] = append(im.storyFeeds[root.feed], feed.ParsedItem{
		GUID: root.url, UID: root.url, Link: root.url, Title: n.Title, Text: feed.StripHTML(mustRender(sb.String())), Markdown: sb.String(),
		Published: time.Unix(n.CreatedAt, 0), RepliesHref: root.url + "/replies.xml", RepliesCount: len(n.Children), Likes: votes,
	})
	im.storyTitles[root.feed] = u.display
	return root
}

func mustRender(markdown string) string {
	h, err := md.Render(markdown)
	if err != nil {
		return markdown
	}
	return h
}

// comment writes a comment as a reply to parent, records it as an approved reply in the parent's
// host, and in the grandparent's cache of second-level replies, then does the same for its children.
func (im *importer) comment(n *node, parent, grand, root *posted, depth int) {
	if depth > im.stats.maxDepth {
		im.stats.maxDepth = depth
	}
	author, text := n.Author, n.Text
	if author == "" || text == "" { // deleted or dead: keep the place in the thread
		author, text = "", "<i>[deleted]</i>"
		im.stats.deleted++
	}
	u := im.user(author)
	markdown := toMarkdown(text)
	me := im.write(u, n, "", markdown, parent, root)
	plain := feed.StripHTML(text)
	published := time.Unix(n.CreatedAt, 0)
	_, err := parent.sp.UpsertReply(store.Reply{PostID: parent.id, Source: me.url, FeedURL: me.feed, Author: u.display, Text: plain, Markdown: markdown,
		Status: store.StatusApproved, Root: root.url, RepliesHref: me.url + "/replies.xml", Published: published})
	if err != nil {
		log.Fatal(err)
	}
	im.stats.replies++
	if grand != nil {
		k := subKey{grand.sp, grand.id, parent.url}
		im.subs[k] = append(im.subs[k], store.Subreply{PostID: grand.id, Via: parent.url, Parent: parent.url, Source: me.url, FeedURL: me.feed,
			Author: u.display, Text: excerpt(plain), Published: published})
	}
	for _, c := range n.Children {
		im.comment(c, me, parent, root, depth+1)
	}
}

func excerpt(s string) string {
	if r := []rune(s); len(r) > 280 {
		return string(r[:280]) + "…"
	}
	return s
}

var (
	reLink   = regexp.MustCompile(`(?is)<a\s[^>]*?href="([^"]*)"[^>]*>(.*?)</a>`)
	rePre    = regexp.MustCompile(`(?is)<pre>\s*(?:<code>)?(.*?)(?:</code>)?\s*</pre>`)
	reItalic = regexp.MustCompile(`(?is)<i>(.*?)</i>`)
	reP      = regexp.MustCompile(`(?i)<p>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlank  = regexp.MustCompile(`\n{3,}`)
)

// toMarkdown converts the small HTML subset Hacker News uses (paragraphs, links, italics, code
// blocks) to markdown. A line starting with ">" after unescaping is already a markdown quote.
func toMarkdown(s string) string {
	s = rePre.ReplaceAllStringFunc(s, func(m string) string {
		code := rePre.FindStringSubmatch(m)[1]
		return "\n\n```\n" + html.UnescapeString(reTag.ReplaceAllString(code, "")) + "\n```\n\n"
	})
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		g := reLink.FindStringSubmatch(m)
		href, label := html.UnescapeString(g[1]), html.UnescapeString(reTag.ReplaceAllString(g[2], ""))
		return "[" + strings.ReplaceAll(label, "]", `\]`) + "](" + strings.ReplaceAll(href, ")", "%29") + ")"
	})
	s = reItalic.ReplaceAllString(s, "*$1*")
	s = reP.ReplaceAllString(s, "\n\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(reBlank.ReplaceAllString(s, "\n\n"))
}
