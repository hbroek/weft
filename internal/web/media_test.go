package web

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hbroek/weft/internal/store"
)

// gpsJPEG is a w by h picture with an EXIF block that holds a position, as a phone writes it.
func gpsJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 120, A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	payload := append([]byte("Exif\x00\x00II*\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00"), []byte("GPSMARKER 40.7128,-74.0060")...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	seg = append(seg, payload...)
	return append(append(append([]byte{}, data[:2]...), seg...), data[2:]...)
}

func uploadAPI(t *testing.T, s *Server, tok, host, query string, data []byte) (int, mediaJSON, string) {
	t.Helper()
	r := httptest.NewRequest("POST", "http://"+host+"/api/media"+query, bytes.NewReader(data))
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var j mediaJSON
	json.Unmarshal(w.Body.Bytes(), &j)
	return w.Code, j, w.Body.String()
}

func TestUploadCleansServesAndCachesAPicture(t *testing.T) {
	s, tok := setup(t)
	host := "alice.localhost:8080"
	code, j, body := uploadAPI(t, s, tok, host, "?alt=A+view", gpsJPEG(t, 300, 200))
	if code != 201 || !strings.HasPrefix(j.Path, "/media/") || !strings.HasSuffix(j.Path, ".jpg") || j.Width != 300 || j.Height != 200 || j.Markdown != "![A view]("+j.Path+")" {
		t.Fatalf("upload: %d %s", code, body)
	}
	if j.URL != "http://"+host+j.Path || !j.Added {
		t.Errorf("url %q added %v", j.URL, j.Added)
	}

	// anyone can read it, with the headers that make it safe and cacheable
	w := do(s, "GET", host, j.Path, "", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(w.Header().Get("Cache-Control"), "immutable") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("serve: %d %v", w.Code, w.Header())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("GPSMARKER")) {
		t.Errorf("the position survived")
	}
	if _, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Errorf("served picture does not decode: %v", err)
	}
	r := httptest.NewRequest("GET", "http://"+host+j.Path, nil)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	nw := httptest.NewRecorder()
	s.ServeHTTP(nw, r)
	if nw.Code != 304 {
		t.Errorf("revalidation: %d", nw.Code)
	}
	// the same picture again is the same address, stored once
	if _, again, _ := uploadAPI(t, s, tok, host, "", gpsJPEG(t, 300, 200)); again.Path != j.Path || again.Added {
		t.Errorf("second upload: %+v", again)
	}
	if w := do(s, "GET", host, "/media/0123456789abcdef0123456789abcdef.jpg", "", ""); w.Code != 404 {
		t.Errorf("missing: %d", w.Code)
	}
	if w := do(s, "GET", host, "/media/..%2fsecret", "", ""); w.Code != 404 {
		t.Errorf("odd name: %d", w.Code)
	}
}

func TestUploadRefusals(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tok, _ := st.AddUser("alice", "Alice", "")
	s := New(Config{BaseDomain: "localhost", MaxImageBytes: 100 << 10, MediaQuota: 150 << 10}, st)
	host := "alice.localhost:8080"

	if code, _, _ := uploadAPI(t, s, "wrong", host, "", gpsJPEG(t, 50, 50)); code != 401 {
		t.Errorf("wrong token: %d", code)
	}
	if code, _, _ := uploadAPI(t, s, tok, host, "", []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>")); code != 415 {
		t.Errorf("svg: %d", code)
	}
	if code, _, _ := uploadAPI(t, s, tok, host, "", bytes.Repeat([]byte("x"), 300<<10)); code != 413 {
		t.Errorf("too large: %d", code)
	}
	if code, _, _ := uploadAPI(t, s, tok, host, "", []byte("\xff\xd8\xff not a jpeg")); code != 422 {
		t.Errorf("corrupt: %d", code)
	}
	// quota: pictures are stored until the user's 150 KB are used, and then refused
	refused := 0
	for i := 0; i < 40 && refused == 0; i++ {
		code, _, body := uploadAPI(t, s, tok, host, "", gpsJPEG(t, 300+i*7, 300))
		switch code {
		case 201:
		case 507:
			refused++
		default:
			t.Fatalf("upload %d: %d %s", i, code, body)
		}
	}
	if refused == 0 {
		t.Errorf("the quota was not enforced")
	}
	if used, _ := s.space("alice").MediaUsed(); used > 150<<10 {
		t.Errorf("%d bytes stored, over the quota", used)
	}
}

func multipartUpload(t *testing.T, s *Server, host, cookie string, data []byte, size string) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "photo.jpg")
	fw.Write(data)
	mw.WriteField("size", size)
	mw.Close()
	r := httptest.NewRequest("POST", "http://"+host+"/media", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", "http://"+host)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestEditorUploadAndCompactSize(t *testing.T) {
	s, _ := setup(t)
	host := "alice.localhost:8080"
	s.space("alice").SetPassword("correct horse")
	lw := httptest.NewRecorder()
	lr := httptest.NewRequest("POST", "http://"+host+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.ServeHTTP(lw, lr)
	ck := cookieOf(lw)

	if w := multipartUpload(t, s, host, "", gpsJPEG(t, 100, 100), "original"); w.Code != 401 {
		t.Errorf("a visitor uploaded: %d", w.Code)
	}
	big := gpsJPEG(t, 2400, 1200)
	w := multipartUpload(t, s, host, ck, big, "compact")
	var j mediaJSON
	json.Unmarshal(w.Body.Bytes(), &j)
	if w.Code != 201 || j.Width != 1600 || j.Height != 800 {
		t.Fatalf("compact: %d %s", w.Code, w.Body)
	}
	w = multipartUpload(t, s, host, ck, big, "original")
	json.Unmarshal(w.Body.Bytes(), &j)
	if w.Code != 201 || j.Width != 2400 {
		t.Fatalf("original: %d %s", w.Code, w.Body)
	}

	// the editor offers it
	page := do3(s, "GET", host, "/compose", ck).Body.String()
	for _, want := range []string{`id="addimg"`, `id="imgsize"`, `id="keepimgs"`, `href="/images"`} {
		if !strings.Contains(page, want) {
			t.Errorf("editor lacks %s", want)
		}
	}
}

func TestPicturesGoOutAsFullAddresses(t *testing.T) {
	s, tok := setup(t)
	host := "alice.localhost:8080"
	_, j, _ := uploadAPI(t, s, tok, host, "", gpsJPEG(t, 80, 60))
	if w := do(s, "POST", host, "/api/posts", tok, `{"markdown":"Look: ![a view](`+j.Path+`)"}`); w.Code != 201 {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}
	abs := "http://" + host + j.Path
	fw := do(s, "GET", host, "/feed.xml", "", "").Body.String()
	if !strings.Contains(fw, `src="`+abs+`"`) && !strings.Contains(fw, `src=&#34;`+abs+`&#34;`) && !strings.Contains(fw, `src=&quot;`+abs+`&quot;`) {
		t.Errorf("the feed's HTML lacks the full address:\n%s", fw)
	}
	if !strings.Contains(fw, "![a view]("+abs+")") {
		t.Errorf("source:markdown lacks the full address:\n%s", fw)
	}
	if strings.Contains(fw, `(`+j.Path+`)`) {
		t.Errorf("the feed still has the bare path")
	}
	// the author's own page keeps the path, so it works wherever the page is served
	var f struct {
		Items []struct {
			Link string `xml:"link"`
		} `xml:"channel>item"`
	}
	_ = f
	home := do(s, "GET", host, "/", "", "").Body.String()
	if !strings.Contains(home, `src="`+j.Path+`"`) {
		t.Errorf("the page lacks the picture:\n%s", home)
	}
	// text/markdown carries full addresses too
	r := httptest.NewRequest("GET", "http://"+host+"/", nil)
	r.Header.Set("Accept", "text/markdown")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "![a view]("+abs+")") {
		t.Errorf("markdown page:\n%s", w.Body)
	}
}

// A reader on another server sees the picture from the author's own host, because the feed says where it is.
func TestPicturesReachFollowersOnOtherServers(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ctx := t.Context()
	code, j, body := uploadAPI(t, alice.s, alice.tok, alice.host, "", gpsJPEG(t, 80, 60))
	if code != 201 {
		t.Fatalf("upload: %d %s", code, body)
	}
	alice.post(t, `{"markdown":"Look: ![a view](`+j.Path+`)"}`)
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	settle(ctx, alice, bob)
	ck := loginCookie(t, bob)
	tl := do3(bob.s, "GET", bob.host, "/timeline", ck).Body.String()
	if !strings.Contains(tl, `src="`+alice.base+j.Path+`"`) {
		t.Errorf("bob's timeline does not load the picture from alice:\n%s", tl)
	}
	if w := do(alice.s, "GET", alice.host, j.Path, "", ""); w.Code != 200 {
		t.Errorf("alice does not serve it: %d", w.Code)
	}
}

func TestKeepCopiesOfPicturesFromOtherSites(t *testing.T) {
	pic := gpsJPEG(t, 120, 90)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(pic)
		case "/x.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Write([]byte("<svg xmlns='http://www.w3.org/2000/svg'/>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.AddUser("alice", "Alice", "")
	s := New(Config{BaseDomain: "localhost", AllowPrivate: true}, st)
	host := "alice.localhost:8080"
	s.space("alice").SetPassword("correct horse")
	lw := httptest.NewRecorder()
	lr := httptest.NewRequest("POST", "http://"+host+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.ServeHTTP(lw, lr)
	ck := cookieOf(lw)

	md := "One ![first](" + remote.URL + "/a.jpg \"A title\") and ![gone](" + remote.URL + "/missing.jpg) and ![vector](" + remote.URL + "/x.svg) and a [link](" + remote.URL + "/a.jpg)."
	r := httptest.NewRequest("POST", "http://"+host+"/media/keep", strings.NewReader(url.Values{"markdown": {md}, "size": {"original"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://"+host)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: ck})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var out struct {
		Markdown string
		Kept     int
		Failed   []keepFailure
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("keep: %d %s", w.Code, w.Body)
	}
	if out.Kept != 1 || len(out.Failed) != 2 {
		t.Fatalf("kept %d, failed %+v", out.Kept, out.Failed)
	}
	m := regexpFind(t, out.Markdown, `!\[first\]\((/media/[0-9a-f]{32}\.jpg) "A title"\)`)
	if !strings.Contains(out.Markdown, "![gone]("+remote.URL+"/missing.jpg)") || !strings.Contains(out.Markdown, "[link]("+remote.URL+"/a.jpg)") {
		t.Errorf("what could not be copied, and plain links, must stay as written:\n%s", out.Markdown)
	}
	// the copy is stored, cleaned, with where it came from
	got := do(s, "GET", host, m, "", "")
	if got.Code != 200 || bytes.Contains(got.Body.Bytes(), []byte("GPSMARKER")) {
		t.Errorf("copy: %d, position kept=%v", got.Code, bytes.Contains(got.Body.Bytes(), []byte("GPSMARKER")))
	}
	page := do3(s, "GET", host, "/images", ck).Body.String()
	if !strings.Contains(page, "copied from") || !strings.Contains(page, remote.URL+"/a.jpg") || !strings.Contains(page, "not used") {
		t.Errorf("images page:\n%s", page)
	}

	// a picture in use is not deleted unless asked twice
	do(s, "POST", host, "/api/posts", "", "") // (no token: nothing)
	tok, _ := st.AddUser("bob", "Bob", "")
	_ = tok
	name := strings.TrimPrefix(m, "/media/")
	sp := s.space("alice")
	u, _ := sp.User()
	sp.AddPost(store.NewPost{UserID: u.ID, Markdown: "![x](" + m + ")", HTML: "<p></p>"})
	del := func(force string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+host+"/images/delete", strings.NewReader(url.Values{"name": {name}, "force": {force}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://"+host)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: ck})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := del(""); w.Header().Get("Location") != "/images?msg=in-use" || do(s, "GET", host, m, "", "").Code != 200 {
		t.Errorf("a picture in use was deleted: %v", w.Header())
	}
	del("1")
	if do(s, "GET", host, m, "", "").Code != 404 {
		t.Errorf("not deleted with force")
	}
}

func regexpFind(t *testing.T, s, pattern string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("%q not found in:\n%s", pattern, s)
	}
	return m[1]
}

// A photo straight from a phone is larger than what is kept; at the Compact size it is accepted and made smaller.
func TestCompactUploadMayBeLargerThanTheLimit(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tok, _ := st.AddUser("alice", "Alice", "")
	s := New(Config{BaseDomain: "localhost", MaxImageBytes: 300 << 10, MaxUploadBytes: 8 << 20, CompactSide: 500}, st)
	host := "alice.localhost:8080"
	m := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	var seed uint32 = 7
	for i := range m.Pix {
		seed = seed*1664525 + 1013904223
		m.Pix[i] = uint8(seed >> 24)
	}
	var b bytes.Buffer
	jpeg.Encode(&b, m, &jpeg.Options{Quality: 90})
	photo := b.Bytes()
	if len(photo) < 1<<20 {
		t.Fatalf("test photo is only %d bytes", len(photo))
	}
	if code, _, _ := uploadAPI(t, s, tok, host, "?size=original", photo); code != 413 {
		t.Errorf("original: %d", code)
	}
	code, j, body := uploadAPI(t, s, tok, host, "?size=compact", photo)
	if code != 201 || j.Width != 500 || j.Size > 300<<10 {
		t.Fatalf("compact: %d %s", code, body)
	}
}

// Posts in a list fold when they are long; a post on its own page does not.
func TestLongPostsFoldInListsOnly(t *testing.T) {
	s, tok := setup(t)
	host := "alice.localhost:8080"
	s.space("alice").SetPassword("correct horse")
	do(s, "POST", host, "/api/posts", tok, `{"markdown":"first"}`)
	var long strings.Builder
	for i := 0; i < 60; i++ {
		long.WriteString("line\\n\\n")
	}
	w := do(s, "POST", host, "/api/posts", tok, `{"markdown":"`+long.String()+`"}`)
	var created struct{ URL string }
	json.Unmarshal(w.Body.Bytes(), &created)

	home := do(s, "GET", host, "/", "", "").Body.String()
	if !strings.Contains(home, `class="e-content clip"`) || !strings.Contains(home, "clipbtn") {
		t.Errorf("a list of posts does not fold long ones:\n%s", home)
	}
	one := do(s, "GET", host, pathOf(created.URL), "", "").Body.String()
	if strings.Contains(one, `class="e-content clip"`) {
		t.Errorf("a post on its own page is folded")
	}
	lw := httptest.NewRecorder()
	lr := httptest.NewRequest("POST", "http://"+host+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	s.ServeHTTP(lw, lr)
	if tl := do3(s, "GET", host, "/timeline", cookieOf(lw)).Body.String(); !strings.Contains(tl, `<div class="clip">`) {
		t.Errorf("the timeline does not fold long posts:\n%s", tl)
	}
}
