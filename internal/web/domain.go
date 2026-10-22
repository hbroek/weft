package web

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// domainView is the "Your address" part of the profile page.
type domainView struct {
	Address           string // where the site is now
	Domain            string // the custom domain, if one is set
	Verified          bool
	TXTName, TXTValue string // the record that shows the domain is the user's
	Target            string // the CNAME target to advertise, if the instance has one
	Points            bool   // the domain's CNAME already points at Target
	Error, Notice     string
}

const txtPrefix = "weft-verify="

func (s *Server) txt(ctx context.Context, name string) ([]string, error) {
	if s.cfg.LookupTXT != nil {
		return s.cfg.LookupTXT(ctx, name)
	}
	return net.DefaultResolver.LookupTXT(ctx, name)
}

func (s *Server) cname(ctx context.Context, name string) (string, error) {
	if s.cfg.LookupCNAME != nil {
		return s.cfg.LookupCNAME(ctx, name)
	}
	return net.DefaultResolver.LookupCNAME(ctx, name)
}

// owns reports whether the domain's DNS carries the claimant's token. A CNAME cannot show this: every
// user's CNAME names the same target, so it proves that somebody controls the domain, not which of the
// users who claimed it.
func (s *Server) owns(ctx context.Context, d *store.Domain) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	txts, err := s.txt(ctx, "_weft."+d.Domain)
	if err != nil {
		return false
	}
	for _, t := range txts {
		if strings.TrimSpace(t) == txtPrefix+d.Token {
			return true
		}
	}
	return false
}

// points reports whether the domain's CNAME is the instance's target.
func (s *Server) points(ctx context.Context, domain string) bool {
	if s.cfg.CNAMETarget == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := s.cname(ctx, domain)
	return err == nil && strings.EqualFold(strings.TrimSuffix(c, "."), strings.TrimSuffix(s.cfg.CNAMETarget, "."))
}

func (s *Server) domainView(r *http.Request, q *req) *domainView {
	v := &domainView{Address: strings.TrimPrefix(strings.TrimPrefix(s.originOf(q.host.Name, r), "https://"), "http://"), Target: s.cfg.CNAMETarget}
	if d, err := s.st.DomainOf(q.host.Name); err == nil {
		v.Domain, v.Verified = d.Domain, d.Verified
		v.TXTName, v.TXTValue = "_weft."+d.Domain, txtPrefix+d.Token
		if !d.Verified {
			v.Points = s.points(r.Context(), d.Domain)
		}
	}
	return v
}

// underBase reports whether a domain is the instance's own base domain or one of its subdomains: those
// belong to the instance, and cannot be claimed.
func (s *Server) underBase(domain string) bool {
	b := strings.ToLower(s.cfg.BaseDomain)
	return domain == b || strings.HasSuffix(domain, "."+b)
}

func (s *Server) domainMsg(w http.ResponseWriter, r *http.Request, q *req, errMsg, notice string) {
	d := s.data(r, q, "Profile")
	d.Profile.Display, d.Profile.Bio, d.Profile.Avatar = q.host.Display, q.host.Bio, q.host.Avatar
	d.Domain = s.domainView(r, q)
	d.Domain.Error, d.Domain.Notice = errMsg, notice
	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusBadRequest
	}
	s.show(w, "profile", status, d)
}

// domainSet claims a custom domain for the owner.
func (s *Server) domainSet(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	domain, err := store.CleanDomain(r.FormValue("domain"))
	if err != nil || s.underBase(domain) {
		s.domainMsg(w, r, q, "That is not a domain name you can use here. Give a name such as weft.example.com.", "")
		return
	}
	if _, err := s.st.SetDomain(q.host.Name, domain); err != nil {
		msg := "Could not save the domain."
		if err == store.ErrDomainTaken {
			msg = "Somebody else already uses that domain."
		}
		s.domainMsg(w, r, q, msg, "")
		return
	}
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
}

// domainCheck looks for the owner's token in the domain's DNS and, if it is there, makes the domain
// the owner's address.
func (s *Server) domainCheck(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	d, err := s.st.DomainOf(q.host.Name)
	if err != nil {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return
	}
	if !s.owns(r.Context(), d) {
		s.domainMsg(w, r, q, "The DNS record is not there yet. Check the name and value, and try again in a few minutes.", "")
		return
	}
	if err := s.st.VerifyDomain(q.host.Name); err != nil {
		msg := "Could not verify the domain."
		if err == store.ErrDomainTaken {
			msg = "Somebody else has shown that they control that domain first."
		}
		s.domainMsg(w, r, q, msg, "")
		return
	}
	// the session cookie belongs to the old address: sign in again at the new one
	_, port := splitHost(r.Host)
	http.Redirect(w, r, s.cfg.Scheme+"://"+d.Domain+port+"/login", http.StatusSeeOther)
}

// domainRemove drops the owner's custom domain; the site is back at the instance's address.
func (s *Server) domainRemove(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if err := s.st.RemoveDomain(q.host.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, port := splitHost(r.Host)
	http.Redirect(w, r, s.cfg.Scheme+"://"+q.host.Name+"."+s.cfg.BaseDomain+port+"/login", http.StatusSeeOther)
}

// Internal is the handler for the private listener the reverse proxy asks before it requests a TLS
// certificate for a host it has not seen (Caddy's on_demand_tls "ask"). It answers 200 for a host this
// instance serves and 404 for any other, so certificates are only ever requested for real users and
// verified domains. Never serve it on a public address.
func (s *Server) Internal() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /domain-ok", func(w http.ResponseWriter, r *http.Request) {
		host, _ := splitHost(r.URL.Query().Get("domain"))
		if host != "" && s.servedHost(host) {
			w.Write([]byte("ok\n"))
			return
		}
		http.NotFound(w, r)
	})
	return mux
}

// servedHost reports whether host is name.<BaseDomain> of a user who is still here, or the verified
// domain of one.
func (s *Server) servedHost(host string) bool {
	if suffix := "." + s.cfg.BaseDomain; strings.HasSuffix(host, suffix) {
		return s.st.UserExists(strings.TrimSuffix(host, suffix))
	}
	_, ok := s.st.HostUser(host)
	return ok
}
