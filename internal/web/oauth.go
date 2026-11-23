package web

import (
	"encoding/json"
	"errors"
	"net/http"
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
