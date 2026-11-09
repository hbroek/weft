package web

import (
	"errors"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// The administration pages. They are for the operator: the accounts that are marked as administrators (weft user
// admin NAME) sign in with their own password. The pages see what an operator needs to run a server (names, states,
// domains, sizes, dates) and never what anyone wrote. They are served on the private listener (-internal) and,
// with -admin-public, at admin.<domain>; see Config.Admin.

const adminCookie = "weft_admin"

// adminPageSize is how many accounts a page of the list holds. Reading when each last posted opens that many files.
const adminPageSize = 50

// adminMux is the router of the administration pages.
func (s *Server) adminMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /login", s.adminLoginForm)
	m.HandleFunc("POST /login", s.adminLogin)
	m.HandleFunc("POST /logout", s.adminOnly(s.adminLogout))
	m.HandleFunc("GET /{$}", s.adminOnly(s.adminOverview))
	m.HandleFunc("GET /accounts", s.adminOnly(s.adminAccounts))
	m.HandleFunc("POST /accounts/new", s.adminOnly(s.adminNewAccount))
	m.HandleFunc("GET /accounts/{name}/remove", s.adminOnly(s.adminConfirmRemove))
	m.HandleFunc("POST /accounts/{name}/{action}", s.adminOnly(s.adminAct))
	m.HandleFunc("GET /waitlist", s.adminOnly(s.adminWaitlist))
	m.HandleFunc("POST /waitlist/{id}/{action}", s.adminOnly(s.adminWaitAct))
	m.HandleFunc("GET /signups", s.adminOnly(s.adminSignups))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin") // not no-referrer: browsers then send "Origin: null" with a form, which the same-origin check refuses
		h.Set("X-Robots-Tag", "noindex")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		m.ServeHTTP(w, r)
	})
}

// adminOnly runs h for a signed-in administrator, and sends anyone else to the sign-in page.
func (s *Server) adminOnly(h func(w http.ResponseWriter, r *http.Request, who string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		who := s.adminSession(r)
		if who == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r, who)
	}
}

// adminSession returns the administrator the request's cookie belongs to, or "". The mark is checked every time, so
// taking it off ends the session at once.
func (s *Server) adminSession(r *http.Request) string {
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return ""
	}
	name, tok, ok := strings.Cut(c.Value, ":")
	if !ok || !s.st.IsAdmin(name) {
		return ""
	}
	sp, release, err := s.st.Space(name)
	if err != nil {
		return ""
	}
	defer release()
	if u, err := sp.UserBySession(tok); err != nil || u.Name != name {
		return ""
	}
	return name
}

func (s *Server) adminLoginForm(w http.ResponseWriter, r *http.Request) {
	s.adminShow(w, "login", http.StatusOK, &adminData{Title: "Sign in"})
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	d := &adminData{Title: "Sign in", Name: name}
	ip, key := s.clientIP(r), "admin:"+name
	if s.lim != nil {
		if blocked, wait := s.lim.logins.blocked(key, ip); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			d.Error = "Too many wrong passwords. Try again in " + waitText(wait) + "."
			s.adminShow(w, "login", http.StatusTooManyRequests, d)
			return
		}
	}
	wrong := func() {
		if s.lim != nil {
			s.lim.logins.fail(key, ip)
		}
		d.Error = "Wrong name or password."
		s.adminShow(w, "login", http.StatusUnauthorized, d)
	}
	if !s.st.IsAdmin(name) {
		wrong() // the same answer for a name that is not an administrator's, so the page tells nothing about names
		return
	}
	sp, release, err := s.st.Space(name)
	if err != nil {
		wrong()
		return
	}
	defer release()
	u, err := sp.CheckPassword(r.FormValue("password"))
	if err != nil {
		wrong()
		return
	}
	if sp.MustChangePassword() {
		d.Error = "This password is a temporary one. Sign in at your own site first and choose a new one."
		s.adminShow(w, "login", http.StatusForbidden, d)
		return
	}
	if s.lim != nil {
		s.lim.logins.ok(key, ip)
	}
	tok, err := sp.CreateSession(u.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: name + ":" + tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: s.cfg.Scheme == "https", MaxAge: int((8 * time.Hour).Seconds())})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request, who string) {
	if c, err := r.Cookie(adminCookie); err == nil {
		if _, tok, ok := strings.Cut(c.Value, ":"); ok {
			if sp, release, err := s.st.Space(who); err == nil {
				sp.DeleteSession(tok)
				release()
			}
		}
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// adminData is what the pages show.
type adminData struct {
	Title, Who, Error, Notice string
	Name                      string // the name typed in a form

	// overview
	Tally                      store.Tally
	Counted, Cap, Left, Wait   int
	NewWait, Pending, Held     int
	Disk                       store.DiskUse
	Free, Total                int64
	HasFree                    bool
	Signups                    bool
	Accounts                   []store.Account
	From, Next, Prev, Count    int
	HasPrev, HasNext           bool
	Lapsing                    []store.Lapse
	Lapsed                     int
	Waiting                    []store.Waiting
	Sign                       []store.Signup
	Failures                   []store.SignupFailure
	SiteBase                   string
	Created                    *createdAccount
	Confirm                    string
	Display, Contact, FromWait string
}

// createdAccount is shown once, after an account is made.
type createdAccount struct {
	Name, Password, Address, Contact string
}

func (s *Server) adminShow(w http.ResponseWriter, name string, code int, d *adminData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := adminTmpl.ExecuteTemplate(w, name, d); err != nil {
		fmt.Fprintf(w, "<p>%v</p>", template.HTMLEscapeString(err.Error()))
	}
}

func (s *Server) adminBase(who string) *adminData {
	return &adminData{Who: who, Cap: s.cfg.MaxUsers, Signups: s.cfg.Signups}
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request, who string) {
	d := s.adminBase(who)
	d.Title = "Overview"
	var err error
	if d.Counted, d.Wait, err = s.st.Counts(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d.Tally, _ = s.st.Tally()
	if d.Cap > 0 {
		d.Left = max(0, d.Cap-d.Counted)
	}
	if list, err := s.st.Waitlist(); err == nil {
		for _, x := range list {
			if !x.Reviewed {
				d.NewWait++
			}
		}
	}
	if p, err := s.st.PendingSignups(); err == nil {
		d.Pending = len(p)
	}
	d.Lapsing, _ = s.st.Lapsing()
	for _, l := range d.Lapsing {
		if l.Lapsed {
			d.Lapsed++
		}
	}
	d.Held = s.st.HeldCount()
	d.Disk = s.st.DiskUse()
	d.Free, d.Total, d.HasFree = diskFree(s.st.DataDir())
	s.adminShow(w, "overview", http.StatusOK, d)
}

// siteBase is the address the accounts' sites are under, such as "http://%s.example.com", or "" if it cannot be told.
func (s *Server) siteBase(r *http.Request) string {
	if h, port := splitHost(r.Host); h == "admin."+s.cfg.BaseDomain {
		return s.cfg.Scheme + "://%s." + s.cfg.BaseDomain + port
	}
	return s.cfg.Scheme + "://%s." + s.cfg.BaseDomain
}

var adminNotices = map[string]string{
	"suspend": "is switched off", "restore": "is back", "remove": "is deleted: the file waits 30 days in the trash",
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request, who string) {
	s.adminAccountsPage(w, r, who, http.StatusOK, &adminData{})
}

func (s *Server) adminAccountsPage(w http.ResponseWriter, r *http.Request, who string, code int, d *adminData) {
	base := s.adminBase(who)
	d.Who, d.Cap, d.Signups, d.Title = base.Who, base.Cap, base.Signups, "Accounts"
	d.From, _ = strconv.Atoi(r.URL.Query().Get("from"))
	d.From = max(0, d.From)
	list, total, err := s.st.Accounts(d.From, adminPageSize)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.st.FillLastPost(list)
	d.Accounts, d.Count = list, total
	d.HasPrev, d.Prev = d.From > 0, max(0, d.From-adminPageSize)
	d.HasNext, d.Next = d.From+adminPageSize < total, d.From+adminPageSize
	d.SiteBase = s.siteBase(r)
	if v, who := r.URL.Query().Get("done"), r.URL.Query().Get("who"); adminNotices[v] != "" && who != "" && d.Notice == "" {
		d.Notice = "The account " + who + " " + adminNotices[v] + "."
	}
	s.adminShow(w, "accounts", code, d)
}

// adminAct switches an account off or on, or deletes it.
func (s *Server) adminAct(w http.ResponseWriter, r *http.Request, who string) {
	name, action := r.PathValue("name"), r.PathValue("action")
	var err error
	switch action {
	case "suspend":
		err = s.st.Suspend(name)
	case "restore":
		err = s.st.Restore(name)
	case "remove":
		if r.FormValue("confirm") != "yes" {
			http.Redirect(w, r, "/accounts/"+url.PathEscape(name)+"/remove", http.StatusSeeOther)
			return
		}
		err = s.st.Remove(name)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		d := &adminData{}
		switch {
		case errors.Is(err, store.ErrAdmin):
			d.Error = name + " is an administrator's account. Take the mark off first (weft user admin -off " + name + ")."
		case errors.Is(err, store.ErrNotFound):
			d.Error = "There is no account called " + name + "."
		default:
			d.Error = "That cannot be done to " + name + " in the state it is in."
		}
		s.adminAccountsPage(w, r, who, http.StatusConflict, d)
		return
	}
	http.Redirect(w, r, "/accounts?done="+action+"&who="+url.QueryEscape(name), http.StatusSeeOther)
}

func (s *Server) adminConfirmRemove(w http.ResponseWriter, r *http.Request, who string) {
	d := s.adminBase(who)
	d.Title, d.Confirm = "Delete an account", r.PathValue("name")
	s.adminShow(w, "confirm", http.StatusOK, d)
}

// adminNewAccount makes an account with a temporary password, within the cap, for a person who was let in.
func (s *Server) adminNewAccount(w http.ResponseWriter, r *http.Request, who string) {
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	d := &adminData{Name: name, Display: strings.TrimSpace(r.FormValue("display"))}
	fromWait, _ := strconv.ParseInt(r.FormValue("waiting"), 10, 64)
	fail := func(code int, msg string) {
		d.Error = msg
		if fromWait > 0 {
			http.Error(w, msg, code)
			return
		}
		s.adminAccountsPage(w, r, who, code, d)
	}
	if counted, _, err := s.st.Counts(); err != nil {
		fail(500, err.Error())
		return
	} else if s.cfg.MaxUsers > 0 && counted >= s.cfg.MaxUsers {
		fail(http.StatusConflict, "All places are taken ("+strconv.Itoa(s.cfg.MaxUsers)+"). Raise -max-users to make room.")
		return
	}
	if _, err := s.st.AddUser(name, d.Display, ""); err != nil {
		fail(http.StatusUnprocessableEntity, err.Error())
		return
	}
	pw := store.GeneratePassword()
	sp, release, err := s.st.Space(name)
	if err == nil {
		err = sp.SetTemporaryPassword(pw)
		release()
	}
	if err != nil {
		fail(500, "the account was made, but its password could not be set: "+err.Error())
		return
	}
	c := &createdAccount{Name: name, Password: pw, Address: fmt.Sprintf(s.siteBase(r), name)}
	if fromWait > 0 {
		s.st.SetReviewed(fromWait, true)
		if list, err := s.st.Waitlist(); err == nil {
			for _, x := range list {
				if x.ID == fromWait {
					c.Contact = x.Contact
				}
			}
		}
	}
	nd := s.adminBase(who)
	nd.Title, nd.Created = "Account made", c
	s.adminShow(w, "created", http.StatusOK, nd)
}

func (s *Server) adminWaitlist(w http.ResponseWriter, r *http.Request, who string) {
	d := s.adminBase(who)
	d.Title = "Waiting list"
	var err error
	if d.Waiting, err = s.st.Waitlist(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d.Counted, _, _ = s.st.Counts()
	if d.Cap > 0 {
		d.Left = max(0, d.Cap-d.Counted)
	}
	s.adminShow(w, "waitlist", http.StatusOK, d)
}

func (s *Server) adminWaitAct(w http.ResponseWriter, r *http.Request, who string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch r.PathValue("action") {
	case "review":
		err = s.st.SetReviewed(id, true)
	case "unreview":
		err = s.st.SetReviewed(id, false)
	case "remove":
		err = s.st.RemoveWaiting(id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/waitlist", http.StatusSeeOther)
}

func (s *Server) adminSignups(w http.ResponseWriter, r *http.Request, who string) {
	d := s.adminBase(who)
	d.Title = "Sign-ups"
	var err error
	if d.Sign, err = s.st.PendingSignups(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d.Failures, _ = s.st.SignupFailures(200)
	s.adminShow(w, "signups", http.StatusOK, d)
}

func humanSize(n int64) string {
	f, units := float64(n), []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for f >= 1000 && i < len(units)-1 {
		f /= 1000
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

var adminTmpl = template.Must(template.New("admin").Funcs(template.FuncMap{
	"size": humanSize,
	"day": func(unix int64) string {
		if unix == 0 {
			return "—"
		}
		return time.Unix(unix, 0).UTC().Format("2006-01-02")
	},
	"dayt":  func(t time.Time) string { return t.UTC().Format("2006-01-02") },
	"when":  func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" },
	"site":  func(base, name string) string { return fmt.Sprintf(base, name) },
	"quote": url.PathEscape,
}).Parse(adminPages))

const adminPages = `
{{define "top"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex"><title>{{.Title}} · Weft administration</title>
<style>
:root{--bg:#eef0fa;--ink:#1a1f3d;--mute:#5b6188;--line:#26337a22;--accent:#26337a;--on:#eef0fa;--card:#fff;--bad:#8c1d18;--badbg:#fbe9e7;--ok:#17552f;--okbg:#e4f4ea}
@media(prefers-color-scheme:dark){:root{--bg:#11142b;--ink:#eef0fa;--mute:#a4abd8;--line:#eef0fa30;--accent:#f08a4b;--on:#11142b;--card:#141937;--bad:#ffb4ab;--badbg:#4a1d1a;--ok:#9fe0b6;--okbg:#17321f}}
body{margin:0;font:16px/1.55 system-ui,sans-serif;background:var(--bg);color:var(--ink)}a{color:var(--accent)}
header{display:flex;flex-wrap:wrap;gap:.5rem 1.2rem;align-items:center;padding:.7rem 16px;border-bottom:1px solid var(--line)}
header nav{display:flex;gap:1rem;flex:1}header form{margin:0}
main{max-width:64rem;margin:0 auto;padding:1.2rem 16px 3rem}h1{font-size:1.6rem;margin:.3rem 0 1rem}h2{font-size:1.15rem;margin:1.6rem 0 .4rem}
table{border-collapse:collapse;width:100%}td,th{text-align:left;padding:.35rem .7rem .35rem 0;border-bottom:1px solid var(--line);vertical-align:top}th{color:var(--mute);font-weight:600;font-size:.9rem}
.num{text-align:right;font-variant-numeric:tabular-nums}.wrap{overflow-x:auto}
button,.btn{font:inherit;cursor:pointer;border:0;background:var(--accent);color:var(--on);padding:.3rem .8rem;border-radius:7px;text-decoration:none;display:inline-block}
button.plain{background:none;color:var(--accent);border:1px solid var(--line)}button.danger{background:var(--bad);color:var(--badbg)}
input{font:inherit;padding:.4rem .5rem;border:1px solid var(--line);border-radius:6px;background:var(--card);color:inherit;max-width:100%}
label{display:block;margin:.5rem 0 .1rem;font-weight:600}.row{display:flex;flex-wrap:wrap;gap:.6rem;align-items:end}.row label{margin:0}
.err{background:var(--badbg);color:var(--bad);padding:.6rem .8rem;border-radius:8px}.ok{background:var(--okbg);color:var(--ok);padding:.6rem .8rem;border-radius:8px}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(11rem,1fr));gap:.8rem}.card{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:.7rem .9rem}
.card b{display:block;font-size:1.6rem;line-height:1.2}.card span{color:var(--mute);font-size:.9rem}small,.mute{color:var(--mute)}code{overflow-wrap:anywhere}
form.inline{display:inline;margin:0}
</style></head><body>
{{if .Who}}<header><b>Weft administration</b><nav><a href="/">Overview</a><a href="/accounts">Accounts</a><a href="/waitlist">Waiting list</a><a href="/signups">Sign-ups</a></nav>
<form method="post" action="/logout"><small>{{.Who}}</small> <button class="plain">Sign out</button></form></header>{{end}}
<main><h1>{{.Title}}</h1>{{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}{{if .Notice}}<p class="ok" role="status">{{.Notice}}</p>{{end}}{{end}}
{{define "bottom"}}</main></body></html>{{end}}

{{define "login"}}{{template "top" .}}
<form method="post" action="/login" style="max-width:22rem"><label for="n">Account name</label><input id="n" name="name" value="{{.Name}}" autocomplete="username" autofocus required>
<label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password" required style="width:100%">
<p><button>Sign in</button></p></form>
<p class="mute">Only accounts marked as administrators can sign in here (<code>weft user admin NAME</code>).</p>{{template "bottom"}}{{end}}

{{define "overview"}}{{template "top" .}}
<div class="cards">
<div class="card"><b>{{.Counted}}{{if .Cap}} / {{.Cap}}{{end}}</b><span>accounts{{if .Cap}} of the cap; {{.Left}} places left{{else}} (no cap){{end}}</span></div>
<div class="card"><b>{{.Tally.Admins}}</b><span>administrators (not counted)</span></div>
<div class="card"><b>{{.Tally.Suspended}}</b><span>switched off</span></div>
<div class="card"><b>{{.Tally.Removed}}</b><span>deleted, in the trash</span></div>
<div class="card"><b>{{.Tally.Moved}}</b><span>moved to other servers</span></div>
<div class="card"><b>{{.Wait}}</b><span>on the <a href="/waitlist">waiting list</a>, {{.NewWait}} not read</span></div>
<div class="card"><b>{{.Pending}}</b><span><a href="/signups">sign-ups</a> waiting for DNS{{if not .Signups}} (sign-up is off){{end}}</span></div>
<div class="card"><b>{{.Lapsed}}</b><span>in the grace period: their domain stopped verifying</span></div>
<div class="card"><b>{{.Held}}</b><span>mentions held for moving accounts</span></div>
</div>
{{if .Lapsing}}<h2>Domains that failed a check</h2>
<div class="wrap"><table><tr><th>Account</th><th>Domain</th><th>State</th><th>Ends</th></tr>
{{range .Lapsing}}<tr><td>{{.Name}}</td><td>{{.Domain}}</td><td>{{if .Lapsed}}lapsed since {{dayt .Since}}{{else}}{{.Fails}} of 3 checks failed{{end}}{{if .Admin}} <small>(administrator: never ended this way)</small>{{end}}</td><td>{{if .Lapsed}}{{dayt .Deadline}}{{else}}—{{end}}</td></tr>{{end}}</table></div>
<p class="mute">Checked once a day: the TXT record and that the domain reaches this server. Three failed checks in a row start the grace period; a pass starts it over.</p>{{end}}
<h2>Disk</h2>
<div class="cards">
<div class="card"><b>{{size .Disk.Spools}}</b><span>users' files</span></div>
<div class="card"><b>{{size .Disk.Instance}}</b><span>shared database</span></div>
<div class="card"><b>{{size .Disk.Trash}}</b><span>trash</span></div>
{{if .HasFree}}<div class="card"><b>{{size .Free}}</b><span>free of {{size .Total}}</span></div>{{end}}
</div>
<p class="mute">Posts are never shown here, and the numbers come from file sizes and the directory.</p>{{template "bottom"}}{{end}}

{{define "accounts"}}{{template "top" .}}
<h2>Make an account</h2>
<form method="post" action="/accounts/new" class="row"><div><label for="nn">Name</label><input id="nn" name="name" value="{{.Name}}" required pattern="[a-z0-9-]+"></div>
<div><label for="nd">Display name (optional)</label><input id="nd" name="display" value="{{.Display}}"></div><button>Make account</button></form>
<p class="mute">It gets a temporary password, shown once. It counts toward the cap{{if .Cap}} ({{.Cap}}){{end}}.</p>
<h2>{{.Count}} accounts</h2>
<div class="wrap"><table><tr><th>Name</th><th>State</th><th>Domain</th><th class="num">Size</th><th>Created</th><th>Last post</th><th></th></tr>
{{$base := .SiteBase}}{{range .Accounts}}<tr><td>{{if or (eq .Status "moved") (eq .Status "removed")}}{{.Name}}{{else}}<a href="{{site $base .Name}}">{{.Name}}</a>{{end}}</td>
<td>{{if .Admin}}administrator{{else}}{{.Status}}{{end}}</td>
<td>{{if .Domain}}{{.Domain}}{{if not .Active}} <small>(not reaching yet)</small>{{end}}{{else}}<span class="mute">—</span>{{end}}</td>
<td class="num">{{if .Bytes}}{{size .Bytes}}{{else}}—{{end}}</td><td>{{day .Created}}</td><td>{{day .LastPost}}</td>
<td>{{if .Admin}}{{else if eq .Status "active"}}<form class="inline" method="post" action="/accounts/{{quote .Name}}/suspend"><button class="plain">Switch off</button></form>
 <a class="btn plain" style="border:1px solid var(--line);background:none;color:var(--accent)" href="/accounts/{{quote .Name}}/remove">Delete…</a>
{{else if or (eq .Status "suspended") (eq .Status "removed")}}<form class="inline" method="post" action="/accounts/{{quote .Name}}/restore"><button class="plain">{{if eq .Status "removed"}}Bring back{{else}}Switch on{{end}}</button></form>
{{if eq .Status "suspended"}} <a class="btn plain" style="border:1px solid var(--line);background:none;color:var(--accent)" href="/accounts/{{quote .Name}}/remove">Delete…</a>{{end}}{{end}}</td></tr>{{end}}
</table></div>
<p>{{if .HasPrev}}<a href="/accounts?from={{.Prev}}">← Earlier</a>{{end}} {{if .HasNext}}<a href="/accounts?from={{.Next}}">Later →</a>{{end}}</p>{{template "bottom"}}{{end}}

{{define "confirm"}}{{template "top" .}}
<p>Delete the account <b>{{.Confirm}}</b>?</p>
<ul><li>Every address of the account answers “gone” at once.</li><li>Its file waits 30 days in the trash. Until then you can bring it back; after that it is deleted for good.</li><li>The name stays taken.</li></ul>
<form method="post" action="/accounts/{{quote .Confirm}}/remove"><input type="hidden" name="confirm" value="yes"><button class="danger">Delete {{.Confirm}}</button> <a href="/accounts">Cancel</a></form>{{template "bottom"}}{{end}}

{{define "created"}}{{template "top" .}}
<p class="ok">The account <b>{{.Created.Name}}</b> is made.</p>
<table><tr><th>Address</th><td><code>{{.Created.Address}}</code></td></tr><tr><th>Temporary password</th><td><code>{{.Created.Password}}</code></td></tr>
{{if .Created.Contact}}<tr><th>Tell</th><td>{{.Created.Contact}} <small>(from the waiting list; marked as read)</small></td></tr>{{end}}</table>
<p>The password is shown once. The person must choose their own at the first sign-in.</p><p><a href="/accounts">Accounts</a> · <a href="/waitlist">Waiting list</a></p>{{template "bottom"}}{{end}}

{{define "waitlist"}}{{template "top" .}}
<p class="mute">{{len .Waiting}} waiting.{{if .Cap}} {{.Left}} of {{.Cap}} places are left.{{end}} There is no promise to anyone on this list. Answer by hand, then remove the request.</p>
{{range .Waiting}}<div class="card" style="margin:.6rem 0"><div class="row"><b>{{.Name}}</b> <span class="mute">{{.Contact}}</span> <small>{{dayt .Created}}{{if .Reviewed}} · read{{else}} · new{{end}}</small></div>
<p style="white-space:pre-wrap;margin:.4rem 0">{{.Links}}</p>{{if .Note}}<p class="mute" style="white-space:pre-wrap;margin:.2rem 0">{{.Note}}</p>{{end}}
<form class="inline" method="post" action="/accounts/new"><input type="hidden" name="name" value="{{.Name}}"><input type="hidden" name="waiting" value="{{.ID}}"><button>Make account {{.Name}}</button></form>
{{if .Reviewed}}<form class="inline" method="post" action="/waitlist/{{.ID}}/unreview"><button class="plain">Mark as new</button></form>{{else}}<form class="inline" method="post" action="/waitlist/{{.ID}}/review"><button class="plain">Mark as read</button></form>{{end}}
<form class="inline" method="post" action="/waitlist/{{.ID}}/remove"><button class="plain">Remove</button></form></div>
{{else}}<p>Nobody is waiting.</p>{{end}}{{template "bottom"}}{{end}}

{{define "signups"}}{{template "top" .}}
<p class="mute">People who chose a name and a domain and have not yet put the record in their DNS. A sign-up lapses after an hour and holds no place in the cap.</p>
<div class="wrap"><table><tr><th>Name</th><th>Domain</th><th>Started</th></tr>
{{range .Sign}}<tr><td>{{.Name}}</td><td>{{.Domain}}</td><td>{{when .Created}}</td></tr>{{else}}<tr><td colspan="3">None waiting.</td></tr>{{end}}</table></div>
<h2>Sign-ups that did not end in an account</h2>
<p class="mute">The name and the domain that were tried, and why it stopped: a form that was refused, a DNS record that was not there, an hour that ran out. The last 1000 entries, nothing older than 30 days. No email addresses and no callers' addresses are kept.</p>
<div class="wrap"><table><tr><th>When</th><th>Name</th><th>Domain</th><th>What</th><th>Why</th></tr>
{{range .Failures}}<tr><td>{{when .At}}</td><td>{{.Name}}</td><td>{{.Domain}}</td><td>{{.Kind}}</td><td>{{.Detail}}</td></tr>{{else}}<tr><td colspan="5">None.</td></tr>{{end}}</table></div>{{template "bottom"}}{{end}}
`
