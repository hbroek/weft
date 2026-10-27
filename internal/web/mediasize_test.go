package web

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// grainyJPEG is a w by h picture that does not compress well, so it is large in bytes like a photograph.
func grainyJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(int64(w*h) + 1))
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := uint8(rng.Intn(120))
			m.SetRGBA(x, y, color.RGBA{R: n + uint8(x*100/w), G: n + uint8(y*100/h), B: n, A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestLargePicturesAreServedInSmallerCopies(t *testing.T) {
	s, tok := setup(t)
	const host = "alice.localhost:8080"
	code, big, body := uploadAPI(t, s, tok, host, "?size=original", grainyJPEG(t, 1600, 1200))
	if code != 201 {
		t.Fatalf("upload: %d %s", code, body)
	}
	orig := do(s, "GET", host, big.Path, "", "")
	if orig.Code != 200 || orig.Body.Len() < 150<<10 {
		t.Fatalf("the test picture is only %d bytes", orig.Body.Len())
	}

	// a list-size copy: smaller, scaled down, and cached for good like the picture
	cp := do(s, "GET", host, big.Path+"?w=800", "", "")
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(cp.Body.Bytes()))
	if cp.Code != 200 || err != nil || cfg.Width != 800 || cfg.Height != 600 || cp.Body.Len() >= orig.Body.Len()/2 {
		t.Fatalf("copy: %d %v %+v %d bytes (original %d)", cp.Code, err, cfg, cp.Body.Len(), orig.Body.Len())
	}
	if cc := cp.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control of a copy: %q", cc)
	}
	etag := cp.Header().Get("ETag")
	if etag == orig.Header().Get("ETag") || etag == "" {
		t.Errorf("a copy needs its own ETag: %q and %q", etag, orig.Header().Get("ETag"))
	}
	r := httptest.NewRequest("GET", "http://"+host+big.Path+"?w=800", nil)
	r.Header.Set("If-None-Match", etag)
	nw := httptest.NewRecorder()
	s.ServeHTTP(nw, r)
	if nw.Code != 304 {
		t.Errorf("revalidation: %d", nw.Code)
	}

	// the thumbnail size, and only the sizes that are offered
	th := do(s, "GET", host, big.Path+"?w=240", "", "")
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(th.Body.Bytes())); err != nil || cfg.Width != 240 {
		t.Errorf("thumbnail: %v %+v", err, cfg)
	}
	for _, bad := range []string{"?w=500", "?w=0", "?w=x", "?w=-1"} {
		if c := do(s, "GET", host, big.Path+bad, "", "").Code; c != 400 {
			t.Errorf("%s: %d, want 400", bad, c)
		}
	}
	if c := do(s, "GET", host, "/media/00000000000000000000000000000000.jpg?w=800", "", "").Code; c != 404 {
		t.Errorf("missing picture: %d", c)
	}

	// a small picture is served as it is, at any size asked for
	_, small, _ := uploadAPI(t, s, tok, host, "", gpsJPEG(t, 300, 200))
	sm := do(s, "GET", host, small.Path+"?w=800", "", "")
	if !bytes.Equal(sm.Body.Bytes(), do(s, "GET", host, small.Path, "", "").Body.Bytes()) {
		t.Errorf("a small picture was changed")
	}

	// a page asks for the list size, links to the picture, and sizes the box to the copy
	js, _ := json.Marshal(map[string]any{"title": "Big", "markdown": "![big](" + big.Path + ")\n\n![small](" + small.Path + ")"})
	if w := do(s, "POST", host, "/api/posts", tok, string(js)); w.Code != 201 {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}
	page := do(s, "GET", host, "/", "", "").Body.String()
	want := `<a href="` + big.Path + `"><img src="` + big.Path + `?w=800" alt="big" width="800" height="600" style="--r:1.3333" decoding="async"></a>`
	if !strings.Contains(page, want) {
		t.Errorf("page lacks %s\n%s", want, regexp.MustCompile(`<img[^>]*>`).FindAllString(page, -1))
	}
	if !strings.Contains(page, `<img src="`+small.Path+`" alt="small" width="300" height="200"`) {
		t.Errorf("the small picture was changed on the page")
	}
}

// Many requests for the same copy make it once.
func TestACopyIsMadeOnceHoweverManyAsk(t *testing.T) {
	c := newScaleCache(1 << 20)
	var mu sync.Mutex
	made := 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := c.get(scaleKey{"a.jpg", 800}, func() *scaled {
				mu.Lock()
				made++
				mu.Unlock()
				return &scaled{data: make([]byte, 100)}
			})
			if v == nil || len(v.data) != 100 {
				t.Errorf("no copy")
			}
		}()
	}
	wg.Wait()
	if made != 1 {
		t.Errorf("made %d times", made)
	}
}

// The memory holds the copies asked for last, and no more than its limit.
func TestScaleCacheKeepsTheRecentOnes(t *testing.T) {
	c := newScaleCache(250)
	for _, n := range []string{"a", "b", "c"} {
		c.get(scaleKey{n, 800}, func() *scaled { return &scaled{data: make([]byte, 100)} })
	}
	if _, ok := c.peek(scaleKey{"a", 800}); ok {
		t.Errorf("the oldest copy is still there")
	}
	if _, ok := c.peek(scaleKey{"c", 800}); !ok || c.bytes > 250 {
		t.Errorf("the newest copy is gone or the cache holds %d bytes", c.bytes)
	}
	if v := c.get(scaleKey{"big", 800}, func() *scaled { return &scaled{data: make([]byte, 500)} }); v == nil {
		t.Errorf("a copy larger than the memory is still answered")
	}
	if _, ok := c.peek(scaleKey{"big", 800}); ok {
		t.Errorf("a copy larger than the memory was kept")
	}
}
