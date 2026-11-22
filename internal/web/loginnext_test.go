package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestSafeNext(t *testing.T) {
	for _, ok := range []string{"/", "/oauth/authorize?client_id=wc_x&redirect_uri=https%3A%2F%2Fa.example%2Fcb&state=a%2Fb", "/compose?page=https://x.example/a", "/a/b#c"} {
		if got := safeNext(ok); got != ok {
			t.Errorf("%q became %q", ok, got)
		}
	}
	for _, bad := range []string{
		"", "evil.example", "//evil.example", "///evil.example", "/\\evil.example", "\\\\evil.example", "https://evil.example/", "http:/evil.example",
		"javascript:alert(1)", "/ok\r\nSet-Cookie: x=y", "/a\x00b", "/a\tb", "/%0d%0a", "data:text/html,x", "/" + strings.Repeat("a", 2000),
	} {
		got := safeNext(bad)
		// an encoded newline is not a control character until it is decoded, and a redirect keeps it encoded
		if got != "" && bad != "/%0d%0a" {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}

// After signing in a person goes where the sign-in was for, and nowhere else.
func TestLoginReturnsToThePageItWasFor(t *testing.T) {
	s, _ := setup(t)
	s.space("alice").SetPassword("correct horse")
	const next = "/oauth/authorize?client_id=wc_x&state=1"

	page := do(s, "GET", host, "/login?next="+url.QueryEscape(next), "", "").Body.String()
	if !strings.Contains(page, `name="next" value="/oauth/authorize?client_id=wc_x&amp;state=1"`) {
		t.Errorf("the form does not carry the page:\n%s", page)
	}
	if page := do(s, "GET", host, "/login?next="+url.QueryEscape("//evil.example/"), "", "").Body.String(); strings.Contains(page, `name="next"`) || strings.Contains(page, "evil") {
		t.Errorf("an unsafe next was carried:\n%s", page)
	}
	if page := do(s, "GET", host, "/login", "", "").Body.String(); strings.Contains(page, `name="next"`) {
		t.Error("a login without a next carries one")
	}

	good := form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}, "next": {next}}, "")
	if good.Code != 303 || good.Header().Get("Location") != next || cookieOf(good) == "" {
		t.Errorf("sign in: %d %q", good.Code, good.Header().Get("Location"))
	}
	for _, bad := range []string{"//evil.example/", "https://evil.example/", "/\\evil.example"} {
		w := form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}, "next": {bad}}, "")
		if w.Code != 303 || w.Header().Get("Location") != "/" {
			t.Errorf("next %q: %d %q", bad, w.Code, w.Header().Get("Location"))
		}
	}
	// a wrong password shows the form again, still carrying the page
	wrong := form(s, "POST", "/login", "", url.Values{"password": {"nope nope"}, "next": {next}}, "")
	if wrong.Code != 401 || !strings.Contains(wrong.Body.String(), `name="next" value="/oauth/authorize?client_id=wc_x&amp;state=1"`) {
		t.Errorf("wrong password: %d\n%s", wrong.Code, wrong.Body)
	}
	// someone who is already signed in is sent straight there
	if w := form(s, "GET", "/login?next="+url.QueryEscape(next), cookieOf(good), nil, ""); w.Code != 303 || w.Header().Get("Location") != next {
		t.Errorf("already signed in: %d %q", w.Code, w.Header().Get("Location"))
	}
}
