package store

import (
	"database/sql"
	"errors"
	"net/mail"
	"os"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// A sign-up with a domain happens in two steps. StartSignup records the name and the domain the person chose and
// gives them a token to put in the domain's DNS; nothing is made yet, and the sign-up lapses after SignupTTL.
// When the token is in the DNS, FinishSignup makes the account, with the domain already verified.

// SignupTTL is how long a sign-up waits for its DNS record.
const SignupTTL = time.Hour

// MaxPendingSignups is how many sign-ups may wait at once, so that a flood of them cannot fill the table.
const MaxPendingSignups = 500

// ErrSignupsBusy is returned when too many sign-ups are waiting.
var ErrSignupsBusy = errors.New("too many sign-ups are waiting")

// Signup is a sign-up that waits for its DNS record.
type Signup struct {
	ID         string // the secret that finds it again; put in the page the person is shown
	Name       string // the account name
	Domain     string // weft.<registered>
	Registered string // the registered domain, which is what one account is counted by
	Email      string // where the owner is warned if the domain stops working; kept in the account
	Token      string // goes in the TXT record of _weft.<Domain>, as weft-verify=<Token>
	Created    time.Time
}

// registeredTaken reports whether an account, or a sign-up that has not lapsed, already holds the registered domain.
func (in *Instance) registeredTaken(reg string) bool {
	rows, err := in.rdb.Query(`SELECT domain FROM domains`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var d string
			if rows.Scan(&d) == nil {
				if r, err := publicsuffix.EffectiveTLDPlusOne(d); err == nil && r == reg {
					return true
				}
			}
		}
	}
	var n int
	in.rdb.QueryRow(`SELECT COUNT(*) FROM signups WHERE registered=? AND created>?`, reg, time.Now().Add(-SignupTTL).Unix()).Scan(&n)
	return n > 0
}

// ErrEmail is returned for an email address that is not a single plain address.
var ErrEmail = errors.New("not an email address")

// MaxEmail is the longest address kept.
const MaxEmail = 254

// CleanEmail checks an email address: one plain address, as in name@example.com, without a display name.
func CleanEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || len(s) > MaxEmail || !strings.Contains(a.Address[strings.LastIndex(a.Address, "@"):], ".") {
		return "", ErrEmail
	}
	return s, nil
}

// StartSignup records a sign-up. The domain is checked with SignupDomain and the name with CheckName; the name
// must be free and the registered domain unused, by an account and by another sign-up that is still waiting.
func (in *Instance) StartSignup(name, domain, email string) (*Signup, error) {
	if err := CheckName(name); err != nil {
		return nil, err
	}
	email, err := CleanEmail(email)
	if err != nil {
		return nil, err
	}
	domain, reg, err := SignupDomain(domain)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	in.db.Exec(`DELETE FROM signups WHERE created<=?`, now.Add(-SignupTTL).Unix())
	var n int
	in.rdb.QueryRow(`SELECT COUNT(*) FROM signups`).Scan(&n)
	if n >= MaxPendingSignups {
		return nil, ErrSignupsBusy
	}
	in.rdb.QueryRow(`SELECT COUNT(*) FROM signups WHERE name=?`, name).Scan(&n)
	if in.UserExists(name) || n > 0 {
		return nil, ErrNameTaken
	}
	if in.registeredTaken(reg) {
		return nil, ErrDomainTaken
	}
	id, err := newToken()
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	if _, err := in.db.Exec(`INSERT INTO signups(id,name,domain,registered,token,email,created) VALUES(?,?,?,?,?,?,?)`,
		id, name, domain, reg, token, email, now.Unix()); err != nil {
		return nil, err
	}
	return &Signup{ID: id, Name: name, Domain: domain, Registered: reg, Token: token, Email: email, Created: now}, nil
}

// SignupByID returns a sign-up that has not lapsed, or ErrNotFound.
func (in *Instance) SignupByID(id string) (*Signup, error) {
	sg := &Signup{ID: id}
	var c int64
	err := in.rdb.QueryRow(`SELECT name,domain,registered,token,email,created FROM signups WHERE id=? AND created>?`,
		id, time.Now().Add(-SignupTTL).Unix()).Scan(&sg.Name, &sg.Domain, &sg.Registered, &sg.Token, &sg.Email, &c)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sg.Created = time.Unix(c, 0)
	return sg, nil
}

// FinishSignup makes the account of a sign-up whose DNS record has been seen: the user, with the password, and the
// domain, already verified. The sign-up is used up. If the account cannot be made nothing is left behind.
func (in *Instance) FinishSignup(sg *Signup, password string) error {
	if len(password) < MinPassword {
		return ErrWeakPassword
	}
	if _, err := in.AddUser(sg.Name, "", ""); err != nil {
		return err
	}
	undo := func() {
		in.db.Exec(`DELETE FROM users WHERE name=?`, sg.Name)
		os.Remove(in.spacePath(sg.Name))
	}
	sp, release, err := in.Space(sg.Name)
	if err != nil {
		undo()
		return err
	}
	err = sp.SetPassword(password)
	if err == nil && sg.Email != "" {
		_, err = sp.db.Exec(`UPDATE users SET email=? WHERE name=?`, sg.Email, sg.Name)
	}
	release()
	if err != nil {
		undo()
		return err
	}
	if _, err := in.db.Exec(`INSERT INTO domains(domain,name,token,verified,bound,created) VALUES(?,?,?,1,1,?)`,
		sg.Domain, sg.Name, sg.Token, time.Now().Unix()); err != nil {
		undo()
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrDomainTaken
		}
		return err
	}
	in.dirs.drop()
	in.db.Exec(`DELETE FROM signups WHERE id=?`, sg.ID)
	return nil
}
