package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchLooksOnlyThroughThisAccount(t *testing.T) {
	s, tok := setup(t)
	btok, err := s.st.AddUser("bob", "Bob", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ host, tok, body string }{
		{alice, tok, `{"title":"Gardening","markdown":"Tomatoes need sun, and a lot of patience in the spring.","keywords":["garden"]}`},
		{alice, tok, `{"markdown":"Nothing about it here."}`},
		{alice, tok, `{"markdown":"100% sure that_is_it"}`},
		{"bob.localhost:8080", btok, `{"markdown":"Bob grows tomatoes too."}`},
	} {
		if w := do(s, "POST", p.host, "/api/posts", p.tok, p.body); w.Code != 201 {
			t.Fatalf("post: %d %s", w.Code, w.Body)
		}
	}
	md := func(q string) string {
		w := getDoc(s, "/search?q="+q, "", "")
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") {
			t.Fatalf("%q: %d %v", q, w.Code, w.Header())
		}
		return w.Body.String()
	}
	got := md("tomatoes")
	if !strings.Contains(got, "1 match for **tomatoes**") || !strings.Contains(got, "[Gardening](http://"+alice+"/posts/gardening.") || !strings.Contains(got, "Tomatoes need sun") || strings.Contains(got, "Bob") {
		t.Errorf("tomatoes:\n%s", got)
	}
	if got := md("PATIENCE+sun"); !strings.Contains(got, "Gardening") { // every word, any case
		t.Errorf("two words:\n%s", got)
	}
	if got := md("patience+moon"); !strings.Contains(got, "0 matches") {
		t.Errorf("a word that is not there:\n%s", got)
	}
	if got := md("garden"); !strings.Contains(got, "Gardening") { // keywords count
		t.Errorf("keyword:\n%s", got)
	}
	if got := md("100%25"); !strings.Contains(got, "1 match") { // % and _ are only themselves
		t.Errorf("percent:\n%s", got)
	}
	if got := md("_"); !strings.Contains(got, "1 match") {
		t.Errorf("underscore:\n%s", got)
	}
	if got := md(""); !strings.Contains(got, "?q=") {
		t.Errorf("no query:\n%s", got)
	}
	// the page: a box to search from on the home page and here, and the matches
	if h := do(s, "GET", alice, "/", "", ""); !strings.Contains(h.Body.String(), `action="/search"`) {
		t.Errorf("home page has no search box")
	}
	page := do(s, "GET", alice, "/search?q=tomatoes", "", "")
	if body := page.Body.String(); page.Code != 200 || !strings.Contains(body, `value="tomatoes"`) || !strings.Contains(body, "1 post matches") || strings.Contains(body, "Bob grows") {
		t.Errorf("page: %d %s", page.Code, body)
	}
	if do(s, "GET", "nobody.localhost:8080", "/search?q=x", "", "").Code != 404 {
		t.Errorf("unknown host")
	}
}

func TestBonbonFileAndItsTemplateMakeAPost(t *testing.T) {
	s, tok := setup(t)
	w := do(s, "GET", alice, "/.well-known/bonbon.json", "", "")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	var f struct {
		URLSearch string
		Mardo     struct {
			NewPages []struct{ Name, Template string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil || f.URLSearch != "http://"+alice+"/search" || len(f.Mardo.NewPages) != 1 || f.Mardo.NewPages[0].Name != "post" {
		t.Fatalf("%v %s", err, w.Body)
	}
	tpl := f.Mardo.NewPages[0].Template
	for _, key := range []string{"title:", "slug:", "keywords:", "canonical:"} {
		if !strings.Contains(tpl, "\n"+key) && !strings.HasPrefix(tpl, "---\n"+key) {
			t.Errorf("template lacks %q", key)
		}
	}
	if strings.Contains(tpl, "in_reply_to") || strings.Contains(tpl, "context") {
		t.Errorf("the template has reply keys: %q", tpl)
	}
	if !strings.HasPrefix(tpl, "---\ntitle: \n") { // the cursor goes on the title
		t.Errorf("template: %q", tpl)
	}
	// the template, as it comes, is a document the server reads; with the title and a body it makes a post
	if _, _, err := parseDoc(tpl + "x"); err != nil {
		t.Errorf("the template does not parse: %v", err)
	}
	r := httptest.NewRequest("POST", "http://"+alice+"/", strings.NewReader(strings.Replace(tpl, "title: \n", "title: From the template\n", 1)+"Body.\n"))
	r.Header.Set("Content-Type", "text/markdown")
	r.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	if rec.Code != 201 || !strings.Contains(rec.Header().Get("Location"), "/posts/from-the-template.") {
		t.Fatalf("create: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}
