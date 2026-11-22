package oauth

import (
	"reflect"
	"strings"
	"testing"
)

func TestPKCE(t *testing.T) {
	// the example of RFC 7636, appendix B
	const verifier, challenge = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if !VerifyPKCE(verifier, challenge) {
		t.Error("the RFC's own example does not verify")
	}
	for name, c := range map[string][2]string{
		"another verifier":              {verifier + "x", challenge},
		"the challenge as the verifier": {challenge, challenge},
		"too short":                     {"short", challenge},
		"bad characters":                {strings.Repeat("a", 42) + " ", challenge},
		"empty challenge":               {verifier, ""},
		"plain method":                  {verifier, verifier},
		"too long":                      {strings.Repeat("a", 129), challenge},
	} {
		if VerifyPKCE(c[0], c[1]) {
			t.Errorf("%s verifies", name)
		}
	}
	if !ValidChallenge(challenge) || ValidChallenge(challenge+"=") || ValidChallenge("") {
		t.Error("ValidChallenge")
	}
}

func TestSecretsAreUniqueAndTagged(t *testing.T) {
	a, b := NewSecret(PrefixAccess), NewSecret(PrefixAccess)
	if a == b || !strings.HasPrefix(a, "wat_") || len(a) != 4+43 {
		t.Errorf("%q %q", a, b)
	}
	if id := NewClientID(); !strings.HasPrefix(id, "wc_") || id == NewClientID() {
		t.Errorf("client id %q", id)
	}
}

func TestScopes(t *testing.T) {
	got, err := ParseScope("post  read post")
	if err != nil || !reflect.DeepEqual(got, []string{"read", "post"}) {
		t.Errorf("%v %v", got, err)
	}
	for _, bad := range []string{"", "  ", "read admin", "READ", "read,post"} {
		if _, err := ParseScope(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !HasScope("read post", "post") || HasScope("read post", "pos") || HasScope("", "read") {
		t.Error("HasScope")
	}
}

func TestValidRedirect(t *testing.T) {
	for _, ok := range []string{
		"https://app.example/cb", "https://app.example:8443/cb?x=1", "http://127.0.0.1/cb", "http://127.0.0.1:53124/cb",
		"http://[::1]:9/cb", "http://localhost:3000/cb", "com.example.app:/oauth", "claude://callback",
	} {
		if err := ValidRedirect(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "/relative", "//evil.example/cb", "http://app.example/cb", "http://127.0.0.1.evil.example/cb", "https://app.example/cb#frag",
		"https://app.example/cb#", "https://user:pw@app.example/cb", "javascript:alert(1)", "data:text/html,x", "file:///etc/passwd",
		"https:///nohost", "https://app.example/a b", "https://app.example/\n", "HTTPS://x", "ftp://app.example/", "app:",
		"https://" + strings.Repeat("a", MaxURI),
	} {
		if err := ValidRedirect(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMatchRedirect(t *testing.T) {
	reg := []string{"https://app.example/cb", "http://127.0.0.1/cb", "http://localhost/cb?x=1", "com.example.app:/oauth"}
	for _, ok := range []string{
		"https://app.example/cb", "http://127.0.0.1/cb", "http://127.0.0.1:53124/cb", "http://localhost:3000/cb?x=1", "com.example.app:/oauth",
	} {
		if !MatchRedirect(reg, ok) {
			t.Errorf("%s does not match", ok)
		}
	}
	for _, bad := range []string{
		"https://app.example/cb/", "https://app.example/cb?x=1", "https://app.example:444/cb", "https://app.example/cb2", "https://APP.example/cb",
		"https://evil.example/cb", "https://app.example.evil.example/cb", "http://127.0.0.1:53124/other", "http://127.0.0.1:53124/cb?y=1",
		"http://localhost:3000/cb", "http://[::1]:5/cb", "http://user@127.0.0.1:5/cb", "http://127.0.0.1:5/cb#f", "https://127.0.0.1:5/cb", "",
		"com.example.app:/oauth2", "com.example.other:/oauth",
	} {
		if MatchRedirect(reg, bad) {
			t.Errorf("%q matches", bad)
		}
	}
	// the port may vary only for a registered http loopback address, never for https
	if MatchRedirect([]string{"https://app.example/cb"}, "https://app.example:8443/cb") {
		t.Error("an https port varied")
	}
}

func TestFormActionSource(t *testing.T) {
	for in, want := range map[string]string{
		"https://app.example/cb?x=1": "https://app.example", "https://app.example:8443/cb": "https://app.example:8443",
		"http://127.0.0.1:5/cb": "http://127.0.0.1:*", "http://[::1]:5/cb": "http://[::1]:*", "http://localhost/cb": "http://localhost:*",
		"com.example.app:/oauth": "com.example.app:",
	} {
		if got := FormActionSource(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestClientMetadata(t *testing.T) {
	if got := CleanName("  My\x00  <b>App</b>\n\tName "); got != "My <b>App</b> Name" { // markup is escaped where it is shown, not here
		t.Errorf("name %q", got)
	}
	if got := CleanName(strings.Repeat("é", 300)); len([]rune(got)) != MaxName {
		t.Errorf("long name has %d characters", len([]rune(got)))
	}
	for _, ok := range []string{"", "https://app.example/", " https://app.example/logo.png "} {
		if _, err := CleanMetaURI(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://app.example/", "javascript:alert(1)", "//app.example/", "https://u:p@app.example/", "https://app.example/a b", "data:image/png;base64,AAAA", "https://" + strings.Repeat("a", MaxURI)} {
		if _, err := CleanMetaURI(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
