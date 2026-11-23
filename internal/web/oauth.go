package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/hbroek/weft/internal/oauth"
	"github.com/hbroek/weft/internal/store"
)

// OAuth pages and documents. Each user is their own authorization server: the issuer is the origin the request came
// to (alice.example.org, or her own domain), so an app that signs in for alice talks only to alice's address. The
// clients are shared by the whole server and live in the instance database, not in anyone's file.

// oauthOnly serves h only where the operator turned OAuth on (-oauth) and the host is an account that is here. With
// OAuth off these addresses do not exist.
func (s *Server) oauthOnly(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := s.hostUser(r.Host)
		if !s.cfg.OAuth || name == "" {
			http.NotFound(w, r)
			return
		}
		if !s.route(w, r, name) {
			return
		}
		sp, release, err := s.st.Space(name) // is there such an account?
		if err != nil {
			http.NotFound(w, r)
			return
		}
		release()
		_ = sp
		h(w, r, name)
	}
}

// oauthCORS lets an app that runs in a browser read the documents and register: nothing here depends on cookies.
func oauthCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Max-Age", "86400")
}

func scopeNames() []string {
	var out []string
	for _, sc := range oauth.Scopes {
		out = append(out, sc.Name)
	}
	return out
}

// authServerMeta is RFC 8414: where an app finds the endpoints, and what they accept.
func (s *Server) authServerMeta(w http.ResponseWriter, r *http.Request, name string) {
	o := s.originOf(name, r)
	oauthCORS(w)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         o,
		"authorization_endpoint":                         o + "/oauth/authorize",
		"token_endpoint":                                 o + "/oauth/token",
		"registration_endpoint":                          o + "/oauth/register",
		"revocation_endpoint":                            o + "/oauth/revoke",
		"scopes_supported":                               scopeNames(),
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"authorization_response_iss_parameter_supported": true,
	})
}

// protectedResourceMeta is RFC 9728: the API is protected, and this is the server that issues the tokens for it.
func (s *Server) protectedResourceMeta(w http.ResponseWriter, r *http.Request, name string) {
	o := s.originOf(name, r)
	oauthCORS(w)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 o,
		"authorization_servers":    []string{o},
		"scopes_supported":         scopeNames(),
		"bearer_methods_supported": []string{"header"},
	})
}

// oauthError answers as RFC 6749 and 7591 say: a status, and a JSON body with a code and a sentence.
func oauthError(w http.ResponseWriter, status int, code, desc string) {
	oauthCORS(w)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (s *Server) oauthPreflight(w http.ResponseWriter, r *http.Request, _ string) {
	oauthCORS(w)
	w.WriteHeader(http.StatusNoContent)
}

// maxRegistration is the most a registration request may hold; real ones are a few hundred bytes.
const maxRegistration = 16 << 10

// registration is what an app says about itself (RFC 7591). Fields that are not listed are ignored.
type registration struct {
	Name         string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	ClientURI    string   `json:"client_uri"`
	LogoURI      string   `json:"logo_uri"`
	PolicyURI    string   `json:"policy_uri"`
	AuthMethod   string   `json:"token_endpoint_auth_method"`
	GrantTypes   []string `json:"grant_types"`
	ResponseType []string `json:"response_types"`
	Scope        string   `json:"scope"`
}

// registerClient lets an app introduce itself. Anyone may, so it does little: it stores what is valid, as given,
// for people to read on the consent page, and gives the app a client_id. Every app is a public client (no secret):
// what protects a code is PKCE, and what protects the user is that they see the app's name and say yes.
func (s *Server) registerClient(w http.ResponseWriter, r *http.Request, _ string) {
	if ct := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]); ct != "application/json" {
		oauthError(w, http.StatusUnsupportedMediaType, "invalid_client_metadata", "send the registration as application/json")
		return
	}
	var in registration
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegistration))
	if err := dec.Decode(&in); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "the body is not a JSON object of at most 16 KB")
		return
	}
	bad := func(code, desc string) { oauthError(w, http.StatusBadRequest, code, desc) }

	if in.AuthMethod != "" && in.AuthMethod != "none" {
		bad("invalid_client_metadata", "only token_endpoint_auth_method \"none\" is supported: apps are public clients that use PKCE")
		return
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			bad("invalid_client_metadata", "grant_types may be authorization_code and refresh_token")
			return
		}
	}
	for _, t := range in.ResponseType {
		if t != "code" {
			bad("invalid_client_metadata", "response_types may be code")
			return
		}
	}
	if in.Scope != "" {
		if _, err := oauth.ParseScope(in.Scope); err != nil {
			bad("invalid_client_metadata", err.Error())
			return
		}
	}
	c := store.OAuthClient{Name: oauth.CleanName(in.Name)}
	if c.Name == "" {
		bad("invalid_client_metadata", "client_name is required")
		return
	}
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > oauth.MaxRedirects {
		bad("invalid_redirect_uri", "give between 1 and 10 redirect_uris")
		return
	}
	seen := map[string]bool{}
	for _, u := range in.RedirectURIs {
		if err := oauth.ValidRedirect(u); err != nil {
			bad("invalid_redirect_uri", err.Error())
			return
		}
		if !seen[u] {
			seen[u] = true
			c.RedirectURIs = append(c.RedirectURIs, u)
		}
	}
	var err error
	for _, f := range []struct {
		in  string
		out *string
	}{{in.ClientURI, &c.ClientURI}, {in.LogoURI, &c.LogoURI}, {in.PolicyURI, &c.PolicyURI}} {
		if *f.out, err = oauth.CleanMetaURI(f.in); err != nil {
			bad("invalid_client_metadata", err.Error())
			return
		}
	}

	saved, err := s.st.AddOAuthClient(c)
	switch {
	case errors.Is(err, store.ErrTooManyClients):
		w.Header().Set("Retry-After", "3600")
		oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "this server is not taking new apps now")
		return
	case err != nil:
		oauthError(w, http.StatusInternalServerError, "server_error", "could not save the registration")
		return
	}
	oauthCORS(w)
	w.Header().Set("Cache-Control", "no-store")
	out := map[string]any{
		"client_id":                  saved.ID,
		"client_id_issued_at":        saved.Created.Unix(),
		"client_name":                saved.Name,
		"redirect_uris":              saved.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	}
	for k, v := range map[string]string{"client_uri": saved.ClientURI, "logo_uri": saved.LogoURI, "policy_uri": saved.PolicyURI} {
		if v != "" {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusCreated, out)
}

// --- the consent page ---

// authRequest is what an app asks for at /oauth/authorize, after it has been checked.
type authRequest struct {
	client      *store.OAuthClient
	redirect    string
	challenge   string
	scope       string
	scopes      []string
	state       string
	responseOK  bool
	challengeOK bool
}

// oauthUser is a page of the signed-in user's own, served only where OAuth is on.
func (s *Server) oauthUser(h func(http.ResponseWriter, *http.Request, *req)) http.HandlerFunc {
	inner := s.user(h)
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.OAuth {
			http.NotFound(w, r)
			return
		}
		inner(w, r)
	}
}

// consentHeaders are for the pages an app can send a person to. They cannot be framed (an app must not be able to
// click "Allow" for the user), load nothing from anywhere else, and may send the form only here and to the app's
// own redirect address, which some browsers check for the redirect that follows.
func consentHeaders(w http.ResponseWriter, redirect string) {
	h := w.Header()
	formAction := "'self'"
	if redirect != "" {
		if src := oauth.FormActionSource(redirect); src != "" {
			formAction += " " + src
		}
	}
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; font-src 'self'; script-src 'none'; form-action "+formAction+"; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
}

// consentToken proves a form came from the consent page that was shown to this browser: it is made from the
// session, which no other site can read.
func consentToken(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte("weft-oauth-consent\x00" + c.Value))
	return hex.EncodeToString(sum[:])
}

// problem shows an error to the person, where the app cannot be told: its address is not one it registered, so
// sending anyone there, even with an error, would help whoever made the request.
func (s *Server) consentProblem(w http.ResponseWriter, r *http.Request, q *req, msg string) {
	consentHeaders(w, "")
	d := s.data(r, q, "Cannot connect")
	d.Error = msg
	s.show(w, "oauth_problem", http.StatusBadRequest, d)
}

// sendBack takes the person back to the app with params added to its redirect address. It is a page that moves on
// by itself, not a 302: browsers disagree on whether form-action applies to the redirect that follows a form, and
// a page that is shown first is the same everywhere. The link is there for a browser that does not follow it.
func (s *Server) sendBack(w http.ResponseWriter, r *http.Request, redirect string, params map[string]string) {
	u, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusInternalServerError)
		return
	}
	v := u.Query()
	for k, val := range params {
		if val != "" {
			v.Set(k, val)
		}
	}
	u.RawQuery = v.Encode()
	consentHeaders(w, redirect)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	to := html.EscapeString(u.String())
	w.Write([]byte(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<meta http-equiv="refresh" content="0;url=` + to + `"><title>Going back to the app</title>` +
		`<p style="font-family:system-ui,sans-serif;margin:2em">Going back to the app. <a href="` + to + `">Continue</a></p></html>`))
}

// readAuthRequest reads and checks an authorization request. It answers the person itself, and returns false, when
// the app or its redirect address cannot be trusted; errors in the rest are for the app, and are in the result.
func (s *Server) readAuthRequest(w http.ResponseWriter, r *http.Request, q *req) (*authRequest, string, bool) {
	a := &authRequest{redirect: r.FormValue("redirect_uri"), challenge: r.FormValue("code_challenge"), state: r.FormValue("state")}
	if len(a.state) > 1000 {
		s.consentProblem(w, r, q, "The request is not valid: the state is too long.")
		return nil, "", false
	}
	c, err := s.st.OAuthClient(r.FormValue("client_id"))
	if err != nil {
		s.consentProblem(w, r, q, "The app that sent you here is not known to this server.")
		return nil, "", false
	}
	if !oauth.MatchRedirect(c.RedirectURIs, a.redirect) {
		s.consentProblem(w, r, q, "The app asked to be sent back to an address it did not register.")
		return nil, "", false
	}
	a.client = c
	scope := r.FormValue("scope")
	if strings.TrimSpace(scope) == "" {
		scope = "read"
	}
	var perr error
	a.scopes, perr = oauth.ParseScope(scope)
	a.scope = strings.Join(a.scopes, " ")
	switch {
	case r.FormValue("response_type") != "code":
		return a, "unsupported_response_type", true
	case !oauth.ValidChallenge(a.challenge) || r.FormValue("code_challenge_method") != "S256":
		return a, "invalid_request", true
	case perr != nil:
		return a, "invalid_scope", true
	}
	return a, "", true
}

// authorize is where an app sends a person to say what it may do for them.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, q *req) {
	a, code, ok := s.readAuthRequest(w, r, q)
	if !ok {
		return
	}
	iss := s.originOf(q.host.Name, r)
	if code != "" {
		s.sendBack(w, r, a.redirect, map[string]string{"error": code, "state": a.state, "iss": iss})
		return
	}
	if !q.owner { // sign in, then come back to this same request
		next := r.URL.RequestURI()
		if safeNext(next) == "" {
			s.consentProblem(w, r, q, "The request is too long to continue after signing in.")
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	consentHeaders(w, a.redirect)
	d := s.data(r, q, "Connect "+a.client.Name)
	ru, _ := url.Parse(a.redirect)
	d.OAuth = &oauthPage{Client: a.client, Redirect: a.redirect, RedirectHost: orStr(ru.Host, ru.Scheme+":"), Challenge: a.challenge, Scope: a.scope, State: a.state, CSRF: consentToken(r)}
	if h := hostOf(a.client.ClientURI); h != "" {
		d.OAuth.ClientHost = h
	}
	for _, name := range a.scopes {
		for _, sc := range oauth.Scopes {
			if sc.Name == name {
				d.OAuth.Scopes = append(d.OAuth.Scopes, sc)
			}
		}
	}
	s.show(w, "oauth_consent", http.StatusOK, d)
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Scheme == "https" {
		return u.Host
	}
	return ""
}

// authorizeAnswer takes the person's answer from the consent page. Everything is checked again: the form's fields
// are only what a browser sent.
func (s *Server) authorizeAnswer(w http.ResponseWriter, r *http.Request, q *req) {
	if !q.owner {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	if want := consentToken(r); want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(r.PostFormValue("csrf"))) != 1 {
		s.consentProblem(w, r, q, "This page has expired. Go back to the app and start again.")
		return
	}
	a, code, ok := s.readAuthRequest(w, r, q)
	if !ok {
		return
	}
	iss := s.originOf(q.host.Name, r)
	back := func(p map[string]string) {
		p["state"], p["iss"] = a.state, iss
		s.sendBack(w, r, a.redirect, p)
	}
	switch {
	case code != "":
		back(map[string]string{"error": code})
	case r.PostFormValue("decision") != "allow":
		back(map[string]string{"error": "access_denied"})
	default:
		grant, err := s.st.CreateOAuthGrant(a.client.ID, q.host.Name, a.scope)
		var c string
		if err == nil {
			c, err = s.st.NewOAuthCode(grant, a.redirect, a.challenge)
		}
		if err != nil {
			back(map[string]string{"error": "server_error"})
			return
		}
		back(map[string]string{"code": c})
	}
}
