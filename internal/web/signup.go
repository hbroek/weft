package web

import (
	"errors"
	"html"
	"log"
	"net/http"
	"strings"

	"github.com/hbroek/weft/internal/store"
)

// Sign-up with a domain, at www.<BaseDomain>/signup, when Config.Signups is on:
//
//	/signup         the form: the domain (weft.<registered domain>) and the account name wanted
//	/signup/check   the DNS record is looked for; when it is there, the person is asked for a password
//	/signup/finish  the account is made
//
// Nothing exists until the last step. The record is looked for again there, so the page of the second step cannot
// be used to skip it.

func esc(s string) string { return html.EscapeString(s) }

// full reports whether the places are all taken.
func (s *Server) full() bool {
	if s.cfg.MaxUsers <= 0 {
		return false
	}
	n, _, err := s.st.Counts()
	return err == nil && n >= s.cfg.MaxUsers
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Signups {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case "/signup":
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			s.signupForm(w, http.StatusOK, "", "", "", "")
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.signupStart(w, r)
	case "/signup/check", "/signup/finish":
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/signup", http.StatusSeeOther)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			reply(w, http.StatusBadRequest, "That did not work", "The form could not be read.")
			return
		}
		sg, err := s.st.SignupByID(r.PostFormValue("id"))
		if err != nil {
			shell(w, http.StatusGone, "This sign-up has lapsed", `<p>A sign-up waits for its DNS record for an hour. <a href="/signup">Start again</a>.</p>`)
			return
		}
		if s.lim != nil {
			if ok, wait := s.lim.dnsCheck.take(s.clientIP(r)); !ok {
				tooMany(w, wait)
				return
			}
		}
		if r.URL.Path == "/signup/check" {
			s.signupCheck(w, r, sg)
		} else {
			s.signupFinish(w, r, sg)
		}
	}
}

func (s *Server) signupForm(w http.ResponseWriter, code int, domain, name, email, errMsg string) {
	if s.full() {
		shell(w, http.StatusOK, "All places are taken", `<p>The number of accounts is limited and they are all in use. You can <a href="/#waitlist">ask for an account</a> on the waiting list, or run your own server.</p>`)
		return
	}
	var b strings.Builder
	if errMsg != "" {
		b.WriteString(`<p class="err">` + esc(errMsg) + `</p>`)
	}
	b.WriteString(`<p>Your site will live at <code>weft.</code> followed by a domain you own, such as <code>weft.example.com</code> or <code>weft.example.co.uk</code>. You prove the domain is yours with a record in its DNS.</p>
<form method="post" action="/signup">
<label for="domain">Your Weft address <small>weft. plus your registered domain, never a name further down.</small></label>
<input id="domain" name="domain" required maxlength="253" placeholder="weft.example.com" value="` + esc(domain) + `" autocapitalize="none" spellcheck="false">
<label for="name">Account name <small>Lowercase letters, digits and hyphens.</small></label>
<input id="name" name="name" required maxlength="31" value="` + esc(name) + `" autocapitalize="none" spellcheck="false" autocomplete="username">
<label for="email">Email address <small>Used only to warn you if your domain stops working, so that you can put it right before the account ends. It is never shown to anyone. Mail is sent only if this server has a way to send it.</small></label>
<input id="email" name="email" type="email" required maxlength="254" value="` + esc(email) + `" autocomplete="email" autocapitalize="none" spellcheck="false">
<button type="submit">Continue</button></form>`)
	shell(w, code, "Sign up with your domain", b.String())
}

func (s *Server) signupStart(w http.ResponseWriter, r *http.Request) {
	if s.lim != nil {
		if ok, wait := s.lim.signup.take(s.clientIP(r)); !ok {
			tooMany(w, wait)
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		reply(w, http.StatusBadRequest, "That did not work", "The form could not be read.")
		return
	}
	domain := strings.ToLower(strings.TrimSpace(r.PostFormValue("domain")))
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	email := strings.TrimSpace(r.PostFormValue("email"))
	fail := func(msg string) { s.signupForm(w, http.StatusBadRequest, domain, name, email, msg) }
	if s.full() {
		s.signupForm(w, http.StatusOK, domain, name, email, "")
		return
	}
	if d, _, err := store.SignupDomain(domain); err == nil && s.underBase(d) {
		fail("That name belongs to this server and cannot be used. Use a domain of your own.")
		return
	}
	sg, err := s.st.StartSignup(name, domain, email)
	switch {
	case err == nil:
		s.signupInstructions(w, sg, "")
	case errors.Is(err, store.ErrEmail):
		fail("The email address does not look right. Give one address, such as you@example.com.")
	case errors.Is(err, store.ErrSignupDomain):
		fail(strings.TrimPrefix(err.Error(), store.ErrSignupDomain.Error()+": "))
	case errors.Is(err, store.ErrInvalid):
		fail(strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
	case errors.Is(err, store.ErrNameTaken):
		fail("That account name is taken, or somebody is signing up with it now. Please choose another.")
	case errors.Is(err, store.ErrDomainTaken):
		fail("That domain already has an account here (one account for each registered domain), or somebody is signing up with it now.")
	case errors.Is(err, store.ErrSignupsBusy):
		reply(w, http.StatusServiceUnavailable, "Busy", "Too many people are signing up right now. Please try again in a little while.")
	default:
		log.Printf("sign-up: %v", err)
		reply(w, http.StatusInternalServerError, "That did not work", "Something went wrong on our side. Please try again later.")
	}
}

// signupInstructions shows what to put in the DNS and a button that looks for it.
func (s *Server) signupInstructions(w http.ResponseWriter, sg *store.Signup, errMsg string) {
	var b strings.Builder
	if errMsg != "" {
		b.WriteString(`<p class="err">` + esc(errMsg) + `</p>`)
	}
	b.WriteString(`<p>Add this record to the DNS of <code>` + esc(sg.Registered) + `</code>. It shows that the domain is yours.</p>
<table><tr><th>Type</th><td>TXT</td></tr><tr><th>Name</th><td><code>_weft.` + esc(sg.Domain) + `</code></td></tr><tr><th>Value</th><td><code>` + esc(txtPrefix+sg.Token) + `</code></td></tr></table>`)
	if t := strings.TrimSuffix(s.cfg.CNAMETarget, "."); t != "" {
		b.WriteString(`<p>And this one, so that the address reaches your site:</p><table><tr><th>Type</th><td>CNAME</td></tr><tr><th>Name</th><td><code>` + esc(sg.Domain) + `</code></td></tr><tr><th>Target</th><td><code>` + esc(t) + `</code></td></tr></table>`)
	} else {
		b.WriteString(`<p>Then point <code>` + esc(sg.Domain) + `</code> at this server, as the operator describes.</p>`)
	}
	b.WriteString(`<p>DNS changes can take a few minutes to spread. When you have added the TXT record, press the button. The account name <code>` + esc(sg.Name) + `</code> is kept for you for an hour.</p>
<form method="post" action="/signup/check"><input type="hidden" name="id" value="` + esc(sg.ID) + `"><button type="submit">I added the record, check it</button></form>`)
	code := http.StatusOK
	if errMsg != "" {
		code = http.StatusUnprocessableEntity
	}
	shell(w, code, "Prove the domain is yours", b.String())
}

func (s *Server) signupPassword(w http.ResponseWriter, sg *store.Signup, errMsg string) {
	var b strings.Builder
	if errMsg != "" {
		b.WriteString(`<p class="err">` + esc(errMsg) + `</p>`)
	}
	b.WriteString(`<p>The record is there. Choose a password for <code>` + esc(sg.Name) + `</code>.</p>
<form method="post" action="/signup/finish"><input type="hidden" name="id" value="` + esc(sg.ID) + `">
<label for="pw">Password <small>At least 8 characters.</small></label>
<input id="pw" name="password" type="password" required minlength="8" autocomplete="new-password">
<label for="pw2">Password again</label>
<input id="pw2" name="password2" type="password" required minlength="8" autocomplete="new-password">
<button type="submit">Make my account</button></form>`)
	code := http.StatusOK
	if errMsg != "" {
		code = http.StatusBadRequest
	}
	shell(w, code, "Choose a password", b.String())
}

func (s *Server) signupCheck(w http.ResponseWriter, r *http.Request, sg *store.Signup) {
	if !s.owns(r.Context(), &store.Domain{Domain: sg.Domain, Token: sg.Token}) {
		s.signupInstructions(w, sg, "The record is not there yet. Check the name and the value, and try again in a few minutes.")
		return
	}
	s.signupPassword(w, sg, "")
}

func (s *Server) signupFinish(w http.ResponseWriter, r *http.Request, sg *store.Signup) {
	pw := r.PostFormValue("password")
	switch {
	case pw != r.PostFormValue("password2"):
		s.signupPassword(w, sg, "The two passwords are not the same.")
		return
	case len(pw) < store.MinPassword:
		s.signupPassword(w, sg, "The password must be at least 8 characters.")
		return
	case !s.owns(r.Context(), &store.Domain{Domain: sg.Domain, Token: sg.Token}):
		s.signupInstructions(w, sg, "The record is not there any more. Add it again, and press the button.")
		return
	case s.full():
		s.signupForm(w, http.StatusOK, "", "", "", "")
		return
	}
	switch err := s.st.FinishSignup(sg, pw); {
	case err == nil:
	case errors.Is(err, store.ErrNameTaken), errors.Is(err, store.ErrDomainTaken):
		reply(w, http.StatusConflict, "Somebody was first", "The name or the domain was taken while you were signing up. Please start again.")
		return
	default:
		log.Printf("sign-up of %s: %v", sg.Name, err)
		reply(w, http.StatusInternalServerError, "That did not work", "Something went wrong on our side. Please try again later.")
		return
	}
	// the domain becomes the address once requests to it arrive here; until then the site is at the server's address
	_, port := splitHost(r.Host)
	at := s.cfg.Scheme + "://" + sg.Name + "." + s.cfg.BaseDomain + port
	active := false
	if d, err := s.st.DomainOf(sg.Name); err == nil && s.points(r.Context(), d.Domain) && s.reaches(r.Context(), d, port) {
		if s.st.ActivateDomain(sg.Name) == nil {
			at, active = s.cfg.Scheme+"://"+d.Domain+port, true
		}
	}
	var b strings.Builder
	b.WriteString(`<p>Your account <code>` + esc(sg.Name) + `</code> is made.</p><p><a href="` + esc(at+"/login") + `">Sign in at ` + esc(strings.TrimPrefix(strings.TrimPrefix(at, "https://"), "http://")) + `</a>.</p>`)
	if !active {
		b.WriteString(`<p>Your site is at that address for now. When the domain <code>` + esc(sg.Domain) + `</code> reaches this server (the CNAME record), open your profile and press <em>Check now</em> under <em>Your address</em>: the domain becomes your address, and the old one redirects to it.</p>`)
	}
	shell(w, http.StatusOK, "Welcome to Weft", b.String())
}
