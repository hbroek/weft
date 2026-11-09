package store

import (
	"strconv"
	"time"
)

// The log of sign-ups that did not end in an account: a form that was refused, a DNS record that was not there when the
// person pressed the button, a sign-up that lapsed unfinished. It holds the name and the domain that were tried and
// why, nothing else: no email address, no address of the caller. It keeps the last MaxSignupFailures entries
// and nothing older than SignupFailureDays.
const (
	MaxSignupFailures = 1000
	SignupFailureDays = 30
)

// Kinds of failure.
const (
	FailRefused  = "refused"   // the form was refused: a rule of the name, the domain or the address
	FailNoRecord = "no record" // the TXT record was not in the DNS when it was looked for
	FailLapsed   = "lapsed"    // the hour ran out before the account was made
	FailFull     = "full"      // there was no place left
	FailError    = "error"     // something went wrong on the server
)

// SignupFailure is a line of the log.
type SignupFailure struct {
	At           time.Time
	Name, Domain string
	Kind, Detail string
}

// LogSignupFailure adds a line to the log. A failure to write it is not worth failing the request for.
func (in *Instance) LogSignupFailure(name, domain, kind, detail string) {
	if len(detail) > 300 {
		detail = detail[:300]
	}
	if len(name) > 64 {
		name = name[:64]
	}
	if len(domain) > 253 {
		domain = domain[:253]
	}
	now := time.Now()
	in.db.Exec(`INSERT INTO signup_failures(at,name,domain,kind,detail) VALUES(?,?,?,?,?)`, now.Unix(), name, domain, kind, detail)
	in.db.Exec(`DELETE FROM signup_failures WHERE at<? OR id<=(SELECT MAX(id) FROM signup_failures)-?`,
		now.Add(-SignupFailureDays*24*time.Hour).Unix(), MaxSignupFailures)
}

// SignupFailures lists the log, newest first.
func (in *Instance) SignupFailures(limit int) ([]SignupFailure, error) {
	rows, err := in.rdb.Query(`SELECT at,name,domain,kind,detail FROM signup_failures ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignupFailure
	for rows.Next() {
		var f SignupFailure
		var at int64
		if err := rows.Scan(&at, &f.Name, &f.Domain, &f.Kind, &f.Detail); err != nil {
			return nil, err
		}
		f.At = time.Unix(at, 0).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// purgeSignups deletes the sign-ups that ran out of time, and logs each one as lapsed.
func (in *Instance) purgeSignups(now time.Time) {
	cut := now.Add(-SignupTTL).Unix()
	rows, err := in.rdb.Query(`SELECT name,domain FROM signups WHERE created<=?`, cut)
	if err == nil {
		var gone [][2]string
		for rows.Next() {
			var n, d string
			if rows.Scan(&n, &d) == nil {
				gone = append(gone, [2]string{n, d})
			}
		}
		rows.Close()
		for _, g := range gone {
			in.LogSignupFailure(g[0], g[1], FailLapsed, "the hour ran out before the account was made")
		}
	}
	in.db.Exec(`DELETE FROM signups WHERE created<=?`, cut)
}

// Setting reads a setting kept in instance.db, or "".
func (in *Instance) Setting(key string) string {
	var v string
	in.rdb.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v
}

// SetSetting keeps a setting.
func (in *Instance) SetSetting(key, value string) error {
	_, err := in.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// The cap on accounts is kept by the server that runs (weft serve -max-users) so that commands run by hand can
// respect it. 0 means no cap.
const settingMaxUsers = "max_users"

// RememberCap records the cap the server runs with.
func (in *Instance) RememberCap(n int) error { return in.SetSetting(settingMaxUsers, strconv.Itoa(n)) }

// Cap returns the cap the server last ran with, and how many accounts count toward it. The cap is 0 for none, or when no
// server has run yet.
func (in *Instance) Cap() (limit, counted int) {
	limit, _ = strconv.Atoi(in.Setting(settingMaxUsers))
	counted, _, _ = in.Counts()
	return
}
