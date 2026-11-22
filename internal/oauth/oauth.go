// Package oauth holds the parts of OAuth 2 that need no storage and no web server: PKCE (RFC 7636), the rules for
// redirect addresses (RFC 6749 and 8252), the scopes, and what a client may say about itself (RFC 7591). The
// rules are here, small and tested, because a mistake in them is a security hole: an open redirect, or a
// consent screen that shows text an attacker chose.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// --- tokens ---

// The prefixes say what a secret is, so that a leaked one is recognised, and so that a bearer token that is not
// one of these is the account's own API token.
const (
	PrefixAccess  = "wat_"
	PrefixRefresh = "wrt_"
	PrefixCode    = "wco_"
)

// NewSecret returns a prefix and 32 random bytes, base64url without padding. Only its hash is ever stored.
func NewSecret(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system has no randomness; nothing safe can be done
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// NewClientID returns an identifier for a client: not secret, but not guessable either.
func NewClientID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "wc_" + base64.RawURLEncoding.EncodeToString(b)
}

// --- PKCE ---

var pkceChars = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

var challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ValidChallenge reports whether c can be an S256 code challenge: 43 characters of base64url, the SHA-256 of a verifier.
func ValidChallenge(c string) bool { return challengeRe.MatchString(c) }

// VerifyPKCE reports whether verifier is the one whose S256 challenge is challenge. Only S256 exists here: the plain
// method protects nothing.
func VerifyPKCE(verifier, challenge string) bool {
	if !pkceChars.MatchString(verifier) || !ValidChallenge(challenge) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// --- scopes ---

// A Scope is one kind of thing an app may be allowed to do for a user.
type Scope struct{ Name, Description string }

// Scopes lists what an app can ask for, in the order the consent page shows them.
var Scopes = []Scope{
	{"read", "Read your posts, your timeline, who you follow, and the replies and likes you have received"},
	{"post", "Write, change and delete your posts, and upload pictures"},
	{"follow", "Follow and unfollow feeds for you"},
	{"like", "Like and unlike for you"},
	{"moderate", "Approve or hide the replies to your posts"},
}

// ParseScope reads a space-separated scope string into the scopes it names, in the order of Scopes and without
// repeats. A scope that does not exist is an error, as is asking for none.
func ParseScope(s string) ([]string, error) {
	want := map[string]bool{}
	for _, f := range strings.Fields(s) {
		ok := false
		for _, sc := range Scopes {
			ok = ok || sc.Name == f
		}
		if !ok {
			return nil, errors.New("unknown scope " + quote(f))
		}
		want[f] = true
	}
	if len(want) == 0 {
		return nil, errors.New("no scope asked for")
	}
	var out []string
	for _, sc := range Scopes {
		if want[sc.Name] {
			out = append(out, sc.Name)
		}
	}
	return out, nil
}

// HasScope reports whether the space-separated scope string grants name.
func HasScope(granted, name string) bool {
	for _, f := range strings.Fields(granted) {
		if f == name {
			return true
		}
	}
	return false
}

func quote(s string) string {
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return `"` + strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s) + `"`
}

// --- redirect addresses ---

// MaxURI is the longest address accepted anywhere in a registration.
const MaxURI = 2000

// MaxRedirects is the most redirect addresses one client may register.
const MaxRedirects = 10

var schemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

// dangerous are the schemes a browser would act on in ways no app needs a redirect for.
var dangerous = map[string]bool{"javascript": true, "data": true, "file": true, "vbscript": true, "about": true, "blob": true, "ftp": true, "ws": true, "wss": true}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidRedirect checks an address a client wants to be sent back to: https; http only to this computer (a
// loopback address, for apps that listen on one); or a private scheme of an installed app (com.example.app:/cb).
// No fragment and no user name or password in any case.
func ValidRedirect(raw string) error {
	if raw == "" || len(raw) > MaxURI || strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("a redirect address is empty, too long, or has spaces")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || !strings.HasPrefix(raw, u.Scheme+":") || u.Fragment != "" || raw[len(raw)-1] == '#' || u.User != nil || u.Opaque != "" {
		return errors.New("a redirect address must be absolute, with no fragment and no user name")
	}
	switch {
	case u.Scheme == "https":
		if u.Hostname() == "" {
			return errors.New("a redirect address needs a host")
		}
	case u.Scheme == "http":
		if !loopback(u.Hostname()) {
			return errors.New("an http redirect address is allowed only to this computer (localhost, 127.0.0.1 or [::1]); use https")
		}
	case !schemeRe.MatchString(u.Scheme) || dangerous[u.Scheme]:
		return errors.New("that redirect scheme is not allowed")
	default:
		if u.Host == "" && u.Path == "" {
			return errors.New("a redirect address needs a path")
		}
	}
	return nil
}

// MatchRedirect reports whether given is one of the registered addresses. The match is exact, character for
// character, with one exception that RFC 8252 makes for apps that listen on a free port of this computer: for a
// registered http address to a loopback host, the port may be anything.
func MatchRedirect(registered []string, given string) bool {
	gu, gerr := url.Parse(given)
	for _, r := range registered {
		if r == given {
			return true
		}
		ru, err := url.Parse(r)
		if err != nil || gerr != nil || ru.Scheme != "http" || gu.Scheme != "http" || !loopback(ru.Hostname()) {
			continue
		}
		if ru.Hostname() == gu.Hostname() && ru.Path == gu.Path && ru.RawQuery == gu.RawQuery && gu.User == nil && gu.Fragment == "" {
			return true
		}
	}
	return false
}

// FormActionSource is what a Content-Security-Policy form-action needs to allow for a redirect address: some
// browsers apply form-action to the redirect that follows a form post. A loopback address gets every port.
func FormActionSource(redirect string) string {
	u, err := url.Parse(redirect)
	if err != nil {
		return ""
	}
	if u.Scheme == "http" && loopback(u.Hostname()) {
		h := u.Hostname()
		if strings.Contains(h, ":") {
			h = "[" + h + "]"
		}
		return "http://" + h + ":*"
	}
	if u.Scheme == "https" {
		return "https://" + u.Host
	}
	return u.Scheme + ":"
}

// --- what a client says about itself ---

// MaxName is the longest client name kept.
const MaxName = 100

// CleanName makes the name a client gives itself fit to show on the consent page: one line of plain text.
func CleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > MaxName {
		s = strings.TrimSpace(string(r[:MaxName]))
	}
	return s
}

// CleanMetaURI checks an address a client gives for its home page, logo or policy: https only, as it is shown
// to people and loaded by their browsers. Empty is allowed and stays empty.
func CleanMetaURI(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > MaxURI || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", errors.New("addresses for a client's page, logo and policy must be https addresses")
	}
	return raw, nil
}
