package store

import (
	"time"
)

// An account that signed up with a domain depends on it. The domain is checked once a day. A check fails when the
// TXT record is gone or the domain does not reach this server. After FailuresToLapse failed checks in a row
// (three days) the domain has lapsed: the owner is warned, and the grace period of GraceDays starts. If the
// domain does not verify again by the end of it, the account is removed, as an administrator would remove it: it
// goes to the trash for TrashDays. A check that succeeds starts everything over. Accounts without a bound domain
// (those let in from the waiting list, or made by the operator) are not checked, and neither are administrators'
// accounts, which are never removed this way.
const (
	CheckEvery      = 24 * time.Hour
	FailuresToLapse = 3
	GraceDays       = 90
	// ReminderDays is how long before the end of the grace period a second warning is sent, where there is a way to send one.
	ReminderDays = 14
)

// checkSlack lets a check run a little early, so that a worker that wakes a few minutes early does not skip a day.
const checkSlack = 2 * time.Hour

// Lapse is where an account's domain stands.
type Lapse struct {
	Name, Domain string
	Active       bool      // the domain is the address
	Fails        int       // failed checks in a row
	Lapsed       bool      // Fails reached FailuresToLapse: the grace period runs
	Since        time.Time // when the domain lapsed (zero until then)
	Deadline     time.Time // when the account ends if the domain has not verified again (zero until it lapsed)
	Mailed       int       // 0 nothing sent, 1 the warning, 2 the reminder
	Admin        bool
}

func lapseOf(l *Lapse, since int64) {
	if since > 0 {
		l.Lapsed = true
		l.Since = time.Unix(since, 0).UTC()
		l.Deadline = l.Since.Add(GraceDays * 24 * time.Hour)
	}
}

// IsBound reports whether the account signed up with its domain.
func (in *Instance) IsBound(name string) bool {
	var n int
	in.rdb.QueryRow(`SELECT COUNT(*) FROM domains WHERE name=? AND bound=1`, name).Scan(&n)
	return n > 0
}

// LapseOf returns where the account's domain stands, or nil when the account is not bound to one or its last check
// passed.
func (in *Instance) LapseOf(name string) *Lapse {
	l := &Lapse{Name: name}
	var active int
	var since int64
	err := in.rdb.QueryRow(`SELECT domain,active,fails,since,mailed FROM domains WHERE name=? AND bound=1 AND fails>0`, name).
		Scan(&l.Domain, &active, &l.Fails, &since, &l.Mailed)
	if err != nil {
		return nil
	}
	l.Active = active == 1
	lapseOf(l, since)
	return l
}

// DomainsDue lists the bound domains whose daily check is due, oldest check first, at most limit.
func (in *Instance) DomainsDue(now time.Time, limit int) ([]*Domain, error) {
	rows, err := in.rdb.Query(`SELECT d.domain,d.name,d.token,d.verified,d.active,d.created FROM domains d JOIN users u ON u.name=d.name
		WHERE d.bound=1 AND d.verified=1 AND u.status IN ('active','suspended') AND u.admin=0 AND d.last_check<=?
		ORDER BY d.last_check LIMIT ?`, now.Add(-(CheckEvery - checkSlack)).Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Domain
	for rows.Next() {
		d := &Domain{}
		var v, a, c int64
		if err := rows.Scan(&d.Domain, &d.Name, &d.Token, &v, &a, &c); err != nil {
			return nil, err
		}
		d.Verified, d.Active, d.Created = v != 0, a != 0, time.Unix(c, 0)
		out = append(out, d)
	}
	return out, rows.Err()
}

// RecordCheck records the outcome of a check. A pass clears the count, the grace period and the mail marks. A failure
// adds one, and the third in a row starts the grace period; newly says that this failure did.
func (in *Instance) RecordCheck(name string, ok bool, now time.Time) (l *Lapse, newly bool, err error) {
	if ok {
		_, err = in.db.Exec(`UPDATE domains SET fails=0,since=0,mailed=0,last_check=? WHERE name=? AND bound=1`, now.Unix(), name)
		return nil, false, err
	}
	tx, err := in.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var fails int
	var since int64
	if err = tx.QueryRow(`SELECT fails,since FROM domains WHERE name=? AND bound=1`, name).Scan(&fails, &since); err != nil {
		return nil, false, ErrNotFound
	}
	fails++
	if fails >= FailuresToLapse && since == 0 {
		since, newly = now.Unix(), true
	}
	if _, err = tx.Exec(`UPDATE domains SET fails=?,since=?,last_check=? WHERE name=? AND bound=1`, fails, since, now.Unix(), name); err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return in.LapseOf(name), newly, nil
}

// Postpone moves the next check of a domain to d from now, without counting a check: for a result that is not to be
// believed, such as every domain failing at once because this server's own DNS is down.
func (in *Instance) Postpone(name string, now time.Time, d time.Duration) error {
	_, err := in.db.Exec(`UPDATE domains SET last_check=? WHERE name=? AND bound=1`, now.Add(d-(CheckEvery-checkSlack)).Unix(), name)
	return err
}

// ClearLapse forgets the failures: the owner showed that the domain works.
func (in *Instance) ClearLapse(name string, now time.Time) error {
	_, _, err := in.RecordCheck(name, true, now)
	return err
}

// MarkMailed records that the warning (1) or the reminder (2) was sent.
func (in *Instance) MarkMailed(name string, level int) error {
	_, err := in.db.Exec(`UPDATE domains SET mailed=? WHERE name=? AND bound=1`, level, name)
	return err
}

// Lapsing lists the accounts whose domain has failed a check, the ones in the grace period first.
func (in *Instance) Lapsing() ([]Lapse, error) {
	rows, err := in.rdb.Query(`SELECT d.name,d.domain,d.active,d.fails,d.since,d.mailed,u.admin FROM domains d JOIN users u ON u.name=d.name
		WHERE d.bound=1 AND d.fails>0 AND u.status IN ('active','suspended') ORDER BY d.since=0, d.since, d.fails DESC, d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Lapse
	for rows.Next() {
		var l Lapse
		var active, admin int
		var since int64
		if err := rows.Scan(&l.Name, &l.Domain, &active, &l.Fails, &since, &l.Mailed, &admin); err != nil {
			return nil, err
		}
		l.Active, l.Admin = active == 1, admin == 1
		lapseOf(&l, since)
		out = append(out, l)
	}
	return out, rows.Err()
}

// EndLapsed removes the accounts whose grace period is over, and returns their names. An administrator's account is
// left alone. The server moves their files to the trash within a minute.
func (in *Instance) EndLapsed(now time.Time) ([]string, error) {
	rows, err := in.rdb.Query(`SELECT d.name FROM domains d JOIN users u ON u.name=d.name
		WHERE d.bound=1 AND d.since>0 AND d.since<=? AND u.status IN ('active','suspended') AND u.admin=0`,
		now.Add(-GraceDays*24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	var done []string
	for _, n := range names {
		if err := in.Remove(n); err == nil {
			done = append(done, n)
		}
	}
	return done, nil
}

// EmailOf is the address kept in the account, which nothing in Weft asks for yet: it is where a mail connector
// would warn the owner. Empty when there is none.
func (in *Instance) EmailOf(name string) string {
	sp, release, err := in.Space(name)
	if err != nil {
		return ""
	}
	defer release()
	var e string
	sp.db.QueryRow(`SELECT email FROM users WHERE name=?`, name).Scan(&e)
	return e
}

// SetEmail keeps an address in the account, where a mail connector can warn its owner.
func (in *Instance) SetEmail(name, email string) error {
	sp, release, err := in.Space(name)
	if err != nil {
		return err
	}
	defer release()
	_, err = sp.db.Exec(`UPDATE users SET email=? WHERE name=?`, email, name)
	return err
}
