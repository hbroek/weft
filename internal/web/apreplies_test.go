package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
)

func TestHTMLFromMastodonBecomesSafeMarkdown(t *testing.T) {
	in := `<p><span class="h-card"><a href="https://x.example/@a" class="u-url mention">@<span>alice</span></a></span> Nice <strong>post</strong> *not em* _x_! ` +
		`See <a href="https://example.org/a_b" rel="nofollow noopener"><span class="invisible">https://</span><span class="ellipsis">example.org/a_b</span></a> ` +
		`<a href="https://x.example/tags/go" class="mention hashtag" rel="tag">#<span>go</span></a></p><p>Second<br>line &amp; <code>code</code></p><blockquote><p>quoted</p></blockquote><script>x()</script>`
	got := stripMentionOf(htmlToMarkdown(in), "alice")
	for _, want := range []string{"Nice **post** \\*not em\\* \\_x\\_!", "[example.org/a\\_b](https://example.org/a_b)", "\\#go", "Second  \nline & `code`", "> quoted"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"@alice", "<script", "x()", "invisible", "https://example.org/a_b)https"} {
		if strings.Contains(got, bad) {
			t.Errorf("has %q in:\n%s", bad, got)
		}
	}
	// other mentions stay, and only a leading mention of the account goes
	if got := stripMentionOf("@alice @carol@x.example hello @alice", "alice"); got != "@carol@x.example hello @alice" {
		t.Errorf("strip: %q", got)
	}
	if got := stripMentionOf("hello", "alice"); got != "hello" {
		t.Errorf("strip: %q", got)
	}
}

func replyActivity(rem *remote, typ, id, inReplyTo, content string, to ...string) []byte {
	note := map[string]any{"id": id, "type": "Note", "attributedTo": rem.actor(), "inReplyTo": inReplyTo, "content": content, "to": to, "published": "2026-11-25T10:00:00Z"}
	b, _ := json.Marshal(map[string]any{"id": id + "/activity", "type": typ, "actor": rem.actor(), "object": note})
	return b
}

func TestRepliesLikesAndBoostsComeInAndGoBackOut(t *testing.T) {
	a, _ := testKeys(t)
	s, tok, rem := federated(t)
	path := createPost(t, s, tok, `{"markdown":"A post to answer."}`)
	flush(s)
	take(t, rem) // the Create
	permalink := "http://" + alice + path
	sp := s.space("alice")
	postID := func() int64 { p, _ := sp.PostBySlug(sp.UID(), strings.TrimPrefix(path, "/posts/")); return p.ID }()
	pending := func() []store.Reply { r, _ := sp.RepliesFor(postID, store.StatusPending); return r }
	likes := func() []store.LikeIn { l, _ := sp.LikesInFor(postID); return l }
	post := func(b []byte) int { return rem.post(s, b, a).Code }
	note := rem.srv.URL + "/users/bob/statuses/1"

	// a public reply is kept, waiting for the owner, as Markdown and not with the mention that addressed the account
	html := `<p><span class="h-card"><a href="http://` + alice + `/" class="u-url mention">@<span>alice</span></a></span> Nice <strong>post</strong>!</p>`
	if c := post(replyActivity(rem, "Create", note, permalink, html, publicAudience)); c != 202 {
		t.Fatalf("reply: %d", c)
	}
	rs := pending()
	if len(rs) != 1 || rs[0].Source != note || !strings.HasPrefix(rs[0].Author, "Bob B (@bob@") || rs[0].Markdown != "Nice **post**!" || rs[0].Status != store.StatusPending || rs[0].Updated != nil {
		t.Fatalf("reply kept as %+v", rs)
	}
	if ap, _ := sp.RepliesFor(postID, store.StatusApproved); len(ap) != 0 {
		t.Errorf("a stranger's reply shows at once")
	}
	// the owner sees it waiting
	if n := sp.PendingReplyCount(sp.UID()); n != 1 {
		t.Errorf("%d replies wait", n)
	}

	// once the owner approves it, it shows under the post with who wrote it, and the tally of replies counts it
	if _, err := sp.SetReplyStatus(sp.UID(), rs[0].ID, store.StatusApproved); err != nil {
		t.Fatal(err)
	}
	page := do(s, "GET", alice, path, "", "").Body.String()
	if !strings.Contains(page, "Nice <strong>post</strong>!") || !strings.Contains(page, "Bob B (@bob@") || !strings.Contains(page, "1 reply") {
		t.Errorf("the approved reply is not on the page")
	}
	sp.SetReplyStatus(sp.UID(), rs[0].ID, store.StatusPending)

	// its edit changes it, and its removal takes it away
	post(replyActivity(rem, "Update", note, permalink, `<p>Better words.</p>`, publicAudience))
	if rs := pending(); len(rs) != 1 || rs[0].Markdown != "Better words." || rs[0].Updated == nil {
		t.Errorf("update: %+v", rs)
	}
	del, _ := json.Marshal(map[string]any{"id": note + "/del", "type": "Delete", "actor": rem.actor(), "object": map[string]any{"id": note, "type": "Tombstone"}})
	if post(del) != 202 || len(pending()) != 0 {
		t.Errorf("a removed reply is kept: %+v", pending())
	}

	// what is not kept: a private reply, a reply to something that is not our post, a reply from an object that is not the sender's
	post(replyActivity(rem, "Create", note+"p", permalink, "<p>secret</p>", id(alice)+"followers"))
	post(replyActivity(rem, "Create", note+"q", "http://"+alice+"/posts/nothing.abcd", "<p>x</p>", publicAudience))
	post(replyActivity(rem, "Create", "http://elsewhere.example/n/1", permalink, "<p>forged</p>", publicAudience))
	post(replyActivity(rem, "Create", note+"r", permalink, "<p>@alice</p>", publicAudience)) // nothing but the mention
	if rs := pending(); len(rs) != 0 {
		t.Errorf("kept what should not be: %+v", rs)
	}
	// a removal of something that is not the sender's does nothing
	post(replyActivity(rem, "Create", note, permalink, "<p>again</p>", publicAudience))
	forged, _ := json.Marshal(map[string]any{"id": "x", "type": "Delete", "actor": rem.actor(), "object": "http://elsewhere.example/n/1"})
	post(forged)
	if len(pending()) != 1 {
		t.Errorf("the reply was lost")
	}

	// likes and boosts are counted, with who; Undo takes them back, by what was undone or by its id
	like := func(typ, lid string, object string) []byte {
		b, _ := json.Marshal(map[string]any{"id": lid, "type": typ, "actor": rem.actor(), "object": object})
		return b
	}
	lid := rem.srv.URL + "/users/bob#likes/1"
	if post(like("Like", lid, permalink)) != 202 || len(likes()) != 1 || !strings.HasPrefix(likes()[0].Author, "Bob B (@bob@") || likes()[0].Trusted {
		t.Fatalf("like: %+v", likes())
	}
	post(like("Like", lid, permalink)) // twice is once
	if tl, _ := sp.Tallies(sp.UID()); tl[postID] != 1 {
		t.Errorf("tally %d", tl[postID])
	}
	post(like("Like", rem.srv.URL+"/users/bob#likes/2", "http://"+alice+"/posts/nothing.abcd"))
	if len(likes()) != 1 {
		t.Errorf("a like of something else was kept")
	}
	post(like("Announce", rem.srv.URL+"/users/bob/statuses/9/activity", permalink))
	if l := likes(); len(l) != 2 || !strings.HasSuffix(l[1].Author, "(boosted)") {
		t.Errorf("boost: %+v", l)
	}
	undo := func(inner any) {
		b, _ := json.Marshal(map[string]any{"id": rem.srv.URL + "/undo/" + lid, "type": "Undo", "actor": rem.actor(), "object": inner})
		post(b)
	}
	undo(map[string]any{"id": lid, "type": "Like", "actor": rem.actor(), "object": permalink})
	if len(likes()) != 1 {
		t.Errorf("undo of the like: %+v", likes())
	}
	undo(rem.srv.URL + "/users/bob/statuses/9/activity") // by id alone
	if len(likes()) != 0 {
		t.Errorf("undo of the boost by id: %+v", likes())
	}
	// an Undo of somebody else's like does nothing
	post(like("Like", lid, permalink))
	b, _ := json.Marshal(map[string]any{"id": "u", "type": "Undo", "actor": rem.actor(), "object": "http://elsewhere.example/likes/1"})
	post(b)
	if len(likes()) != 1 {
		t.Errorf("the like was lost")
	}
}

func id(host string) string { return "http://" + host + "/" }

// Servers hold many actors: another account on bob's server may not change or take back what bob sent, even knowing its address.
func TestAnotherActorOnTheSameServerCannotChangeWhatBobSent(t *testing.T) {
	a, _ := testKeys(t)
	s, tok, rem := federated(t)
	path := createPost(t, s, tok, `{"markdown":"A post to answer."}`)
	flush(s)
	take(t, rem)
	permalink := "http://" + alice + path
	sp := s.space("alice")
	p, _ := sp.PostBySlug(sp.UID(), strings.TrimPrefix(path, "/posts/"))
	carol := rem.srv.URL + "/users/carol"
	asCarol := func(act map[string]any) int {
		act["actor"] = carol
		b, _ := json.Marshal(act)
		return deliver(s, signedPost(alice, "/inbox", b, carol+"#main-key", a, time.Now(), allSigned)).Code
	}
	note := rem.srv.URL + "/users/bob/statuses/1"
	lid := rem.srv.URL + "/users/bob#likes/1"
	rem.post(s, replyActivity(rem, "Create", note, permalink, "<p>bob's words</p>", publicAudience), a)
	lk, _ := json.Marshal(map[string]any{"id": lid, "type": "Like", "actor": rem.actor(), "object": permalink})
	rem.post(s, lk, a)

	// carol's attempts, each with the right address and a valid signature of her own
	asCarol(map[string]any{"id": "d1", "type": "Delete", "object": note})
	asCarol(map[string]any{"id": "u1", "type": "Undo", "object": lid})
	asCarol(map[string]any{"id": "u2", "type": "Undo", "object": map[string]any{"id": lid, "type": "Like"}})
	asCarol(map[string]any{"id": "c1", "type": "Update", "object": map[string]any{"id": note, "type": "Note", "attributedTo": carol, "inReplyTo": permalink, "content": "<p>carol was here</p>", "to": []string{publicAudience}}})
	asCarol(map[string]any{"id": lid, "type": "Like", "object": permalink}) // a like of hers under the id bob's like has
	rs, _ := sp.RepliesFor(p.ID, store.StatusPending)
	if len(rs) != 1 || rs[0].Markdown != "bob's words" {
		t.Errorf("bob's reply was changed: %+v", rs)
	}
	if ls, _ := sp.LikesInFor(p.ID); len(ls) != 1 || !strings.Contains(ls[0].Author, "bob") {
		t.Errorf("bob's like was changed: %+v", ls)
	}
	// bob still can
	del, _ := json.Marshal(map[string]any{"id": "d2", "type": "Delete", "actor": rem.actor(), "object": note})
	rem.post(s, del, a)
	if rs, _ := sp.RepliesFor(p.ID, store.StatusPending); len(rs) != 0 {
		t.Errorf("bob cannot remove his own reply")
	}
}

// What a reply holds in a code block cannot end the block and become Markdown of its own.
func TestACodeBlockCannotEndItsFence(t *testing.T) {
	md := htmlToMarkdown("<pre>a\n```\n![x](https://tracker.example/p.png)\n```\nb</pre><p>after</p>")
	if !strings.HasPrefix(md, "````\n") || !strings.Contains(md, "\n````\n\nafter") {
		t.Errorf("the fence is not longer than the text in it:\n%s", md)
	}
	if longestRun("a```b``c", '`') != 3 || longestRun("", '`') != 0 {
		t.Errorf("longestRun")
	}
}

// Nothing in inline code or a link may carry a line break that would end the paragraph and free the text after it.
func TestInlineCodeAndLinksStayOnOneLine(t *testing.T) {
	md := htmlToMarkdown("<p>a <code>x\n\n![t](https://tracker.example/p.png)\n\ny</code> and <a href=\"https://e.example/p\">la\n\nbel</a> and <a href=\"https://e.example/p\nq\">broken</a></p>")
	if strings.Contains(md, "\n") {
		t.Errorf("a line break inside the paragraph:\n%q", md)
	}
	for _, want := range []string{"`x ![t](https://tracker.example/p.png) y`", "[la bel](https://e.example/p)", " and broken"} {
		if !strings.Contains(md, want) {
			t.Errorf("lacks %q in %q", want, md)
		}
	}
	if strings.Contains(md, "](https://e.example/p%0A") || strings.Contains(md, "[broken]") { // an address with a line break makes no link
		t.Errorf("a link with a broken address: %q", md)
	}
}

// "!" before a link would make it an image; other markup characters at the start of a line are text too.
func TestAStrangersTextCannotBecomeAnImageOrAList(t *testing.T) {
	md := htmlToMarkdown(`<p>!<a href="https://tracker.example/p.png">x</a></p><p>1. not a list<br>- not one<br>+ nor this<br>a = b | c</p><p>![i](https://t.example/i.png)</p>`)
	if strings.Contains(md, "![") && !strings.Contains(md, `\![`) {
		t.Errorf("an image: %q", md)
	}
	if strings.Contains(md, "\n![") || strings.HasPrefix(md, "![") || strings.Contains(md, "(https://t.example/i.png)") && !strings.Contains(md, `\[i\]`) {
		t.Errorf("an image from text: %q", md)
	}
	for _, want := range []string{`\![x](https://tracker.example/p.png)`, `1\. not a list`, `\- not one`, `\+ nor this`, `a \= b \| c`} {
		if !strings.Contains(md, want) {
			t.Errorf("lacks %q in %q", want, md)
		}
	}
	// the rendered reply has no <img> at all
	if h := string(renderRemote(md, "")); strings.Contains(h, "<img") || strings.Contains(h, "<ul") || strings.Contains(h, "<ol") {
		t.Errorf("rendered: %s", h)
	}
}
