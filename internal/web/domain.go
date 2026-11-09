package web

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/fetch"
	"github.com/hbroek/weft/internal/store"
)

// domainView is the "Your address" part of the profile page.
type domainView struct {
	Address           string // where the site is now
	Domain            string // the custom domain, if one is set
	Verified          bool   // the TXT record is in place
	Active            bool   // the domain reaches this server, so it is the address
	TXTName, TXTValue string // the record that shows the domain is the user's
	Target            string // the CNAME target to advertise, if the instance has one
	Points            bool   // the domain's CNAME already points at Target
	Dev               bool   // the server is in development mode: reachable only from this computer
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
	v := &domainView{Address: strings.TrimPrefix(strings.TrimPrefix(s.originOf(q.host.Name, r), "https://"), "http://"), Target: s.cfg.CNAMETarget, Dev: s.cfg.AllowPrivate}
	if d, err := s.st.DomainOf(q.host.Name); err == nil {
		v.Domain, v.Verified, v.Active = d.Domain, d.Verified, d.Active
		v.TXTName, v.TXTValue = "_weft."+d.Domain, txtPrefix+d.Token
		if !d.Active {
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
	d.Move = s.moveView(r, q)
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

// domainProof answers a request on a custom domain with the claim's token, which a domain that is verified but not
// yet the address is asked for to show that requests to it arrive here. The token is in the domain's public DNS
// already, so this tells nobody anything new.
func (s *Server) domainProof(w http.ResponseWriter, r *http.Request, q *req) {
	d, err := s.st.DomainOf(q.host.Name)
	if h, _ := splitHost(r.Host); err != nil || !d.Verified || h != d.Domain {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, proofPrefix+d.Token+"\n")
}

const proofPrefix = "weft-domain="

// reaches asks the domain itself whether its requests arrive at this server, for this user.
func (s *Server) reaches(ctx context.Context, d *store.Domain, port string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body string
	var err error
	if s.cfg.ReachDomain != nil {
		body, err = s.cfg.ReachDomain(ctx, d.Domain)
	} else {
		var res *fetch.Result
		if res, err = s.web.Get(ctx, s.cfg.Scheme+"://"+d.Domain+port+"/.well-known/weft-domain"); err == nil {
			body = string(res.Body)
		}
	}
	return err == nil && strings.TrimSpace(body) == proofPrefix+d.Token
}

// domainCheck is the owner's "Check now". First it looks for the owner's token in the domain's DNS and, if it
// is there, marks the domain verified. Then it asks the domain whether it reaches this server; only if it does
// does the domain become the address, and the owner is sent to sign in there. Until then nothing changes
// for visitors or for the owner.
func (s *Server) domainCheck(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	d, err := s.st.DomainOf(q.host.Name)
	if err != nil {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return
	}
	if d.Active { // already the address: see that it still works, and fall back if it does not
		_, port := splitHost(r.Host)
		if s.reaches(r.Context(), d, port) {
			s.domainMsg(w, r, q, "", "The domain reaches this server.")
			return
		}
		if err := s.st.DeactivateDomain(q.host.Name); err != nil {
			s.domainMsg(w, r, q, "Could not go back to the old address.", "")
			return
		}
		s.domainMsg(w, r, q, "", "The domain no longer reaches this server, so your site is back at the old address. The domain stays verified: when it reaches this server again, check it and it becomes your address again.")
		return
	}
	if !d.Verified {
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
		d.Verified = true
	}
	_, port := splitHost(r.Host) // kept for development, where the server is reached on one
	if !s.reaches(r.Context(), d, port) {
		s.domainMsg(w, r, q, "", "The domain is verified, but it does not reach this server yet, so your site stays at "+
			strings.TrimPrefix(strings.TrimPrefix(s.originOf(q.host.Name, r), "https://"), "http://")+" for now. Point the domain here (see below), wait for the change to spread, and check again.")
		return
	}
	if err := s.st.ActivateDomain(q.host.Name); err != nil {
		s.domainMsg(w, r, q, "Could not switch to the domain.", "")
		return
	}
	// the session cookie belongs to the old address: sign in again at the new one
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

// Internal is the handler for the private listener. The reverse proxy asks it, before it requests a TLS
// certificate for a host it has not seen (Caddy's on_demand_tls "ask"), whether the host is served: 200
// for a host this instance serves and 404 for any other, so certificates are only ever requested for
// real users and verified domains. A monitor reads /metrics from it. Never serve it on a public address.
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
	mux.HandleFunc("GET /metrics", s.metrics)
	if s.cfg.Admin {
		mux.Handle("/", s.adminMux()) // the administration pages: anything the two lines above do not answer
	}
	return mux
}

// metrics writes the instance's numbers in the Prometheus text format.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	st := s.st.Stats()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var b strings.Builder
	m := func(kind, name, help string, v any) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, kind, name, v)
	}
	m("gauge", "weft_pool_open", "User databases open now.", st.PoolOpen)
	m("gauge", "weft_pool_in_use", "Open user databases a request is using.", st.PoolInUse)
	m("gauge", "weft_pool_cap", "Most user databases kept open at once.", st.PoolCap)
	m("counter", "weft_pool_hits_total", "Requests that found their user's database open.", st.PoolHits)
	m("counter", "weft_pool_opens_total", "User databases opened.", st.PoolOpens)
	m("counter", "weft_pool_evictions_total", "User databases closed to stay within the cap.", st.PoolEvictions)
	m("counter", "weft_pool_idle_closes_total", "User databases closed for being idle.", st.PoolIdle)
	m("gauge", "weft_instance_reads_in_use", "Read connections of instance.db in use.", st.ReadsInUse)
	m("counter", "weft_instance_read_wait_seconds_total", "Time spent waiting for a read connection of instance.db.", st.ReadWait.Seconds())
	m("gauge", "weft_instance_writes_in_use", "1 while the write connection of instance.db is in use.", st.WriteInUse)
	m("counter", "weft_instance_write_wait_seconds_total", "Time spent waiting for the write connection of instance.db.", st.WriteWait.Seconds())
	m("gauge", "weft_users", "Users, those who moved away included.", st.Users)
	m("gauge", "weft_users_moved", "Users who moved away.", st.Moved)
	m("gauge", "weft_trash_files", "Files of moved users waiting to be deleted.", st.TrashFiles)
	m("gauge", "weft_open_files_limit", "The process's limit on open files (0: unknown).", st.FileLimit)
	io.WriteString(w, b.String())
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
