package web

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func getEnc(s *Server, host, path, enc string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://"+host+path, nil)
	if enc != "" {
		r.Header.Set("Accept-Encoding", enc)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// Pages, styles and feeds are compressed for a client that accepts it, and only then; pictures never are.
func TestTextIsCompressedForClientsThatAcceptIt(t *testing.T) {
	s, tok := setup(t)
	const host = "alice.localhost:8080"
	_, pic, _ := uploadAPI(t, s, tok, host, "", gpsJPEG(t, 300, 200))
	js, _ := json.Marshal(map[string]any{"title": "Hello", "markdown": strings.Repeat("a line of text for the page\n\n", 50)})
	do(s, "POST", host, "/api/posts", tok, string(js))

	plain := getEnc(s, host, "/", "")
	if plain.Header().Get("Content-Encoding") != "" || plain.Header().Get("Vary") != "" && !strings.Contains(plain.Header().Get("Vary"), "Accept") {
		t.Errorf("an uncompressed answer is marked: %v", plain.Header())
	}
	for _, path := range []string{"/", "/feed.xml", "/static/weft.css"} {
		want := getEnc(s, host, path, "").Body.Bytes()
		w := getEnc(s, host, path, "gzip, deflate, br")
		if w.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(w.Header().Get("Vary"), "Accept-Encoding") || w.Header().Get("Content-Length") != "" {
			t.Errorf("%s: headers %v", path, w.Header())
			continue
		}
		zr, err := gzip.NewReader(w.Body)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		got, _ := io.ReadAll(zr)
		if string(got) != string(want) {
			t.Errorf("%s: the decompressed answer differs (%d and %d bytes)", path, len(got), len(want))
		}
		if w.Body.Len() >= len(want) {
			t.Errorf("%s: %d bytes compressed, %d before", path, w.Body.Len(), len(want))
		}
	}
	if w := getEnc(s, host, pic.Path, "gzip"); w.Header().Get("Content-Encoding") != "" {
		t.Errorf("a picture was compressed")
	}
	if w := getEnc(s, host, "/", "gzip;q=0"); w.Header().Get("Content-Encoding") != "" {
		t.Errorf("gzip;q=0 means no")
	}
}

// Every page says what it is about, for search results and link previews.
func TestPagesHaveAMetaDescription(t *testing.T) {
	s, tok := setup(t)
	const host = "alice.localhost:8080"
	home := getEnc(s, host, "/", "").Body.String()
	if !strings.Contains(home, `<meta name="description" content="Notes on the open web.">`) {
		t.Errorf("home:\n%s", home[:600])
	}
	js, _ := json.Marshal(map[string]any{"title": "Dark matter", "markdown": "It is **everywhere** & <b>nowhere</b>, as the saying goes. " + strings.Repeat("More and more words follow here. ", 20)})
	w := do(s, "POST", host, "/api/posts", tok, string(js))
	var res struct{ URL string }
	json.Unmarshal(w.Body.Bytes(), &res)
	page := getEnc(s, host, strings.TrimPrefix(res.URL, "http://"+host), "").Body.String()
	i := strings.Index(page, `<meta name="description" content="`)
	if i < 0 {
		t.Fatalf("post page has no description:\n%s", page[:600])
	}
	desc := page[i+len(`<meta name="description" content="`):]
	desc = desc[:strings.Index(desc, `"`)]
	if !strings.HasPrefix(desc, "It is everywhere &amp; nowhere, as the saying goes.") || strings.Contains(desc, "<") || len([]rune(desc)) > 260 {
		t.Errorf("description of a post: %q", desc)
	}
}

func TestDescribeShortensAtAWord(t *testing.T) {
	if got := describe("<p>Short &amp; sweet</p>"); got != "Short & sweet" {
		t.Errorf("%q", got)
	}
	long := describe(strings.Repeat("word ", 100))
	if r := []rune(long); len(r) > 161 || !strings.HasSuffix(long, "word…") {
		t.Errorf("%d runes: %q", len(r), long)
	}
}
