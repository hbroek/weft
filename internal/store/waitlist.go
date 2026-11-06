package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxWaiting is how many people the waiting list holds; past that it is closed, so a flood of requests cannot
// fill the disk.
const MaxWaiting = 5000

// ErrWaitlistFull is returned when the waiting list is closed because it is full.
var ErrWaitlistFull = errors.New("the waiting list is full")

// ErrOnList is returned when the name or the address is already on the waiting list.
var ErrOnList = errors.New("already on the waiting list")

// Waiting is one request for an account.
type Waiting struct {
	ID      int64
	Name    string // the account name wanted
	Contact string // an email address, used only to answer
	Links   string // where the person writes: profiles, a blog, posts
	Note    string
	Created time.Time
	// Reviewed is set when the operator has read the request; the request stays on the list until it is answered.
	Reviewed bool
}

// JoinWaitlist adds a request. The name must be a name an account could have and must not be taken; the
// contact address and the name can each be on the list once.
func (in *Instance) JoinWaitlist(name, contact, links, note string) error {
	if err := CheckName(name); err != nil {
		return err
	}
	if _, err := in.UserByName(name); err == nil {
		return ErrNameTaken
	}
	contact = strings.TrimSpace(contact)
	var n int
	if err := in.rdb.QueryRow(`SELECT COUNT(*) FROM waitlist`).Scan(&n); err != nil {
		return err
	}
	if n >= MaxWaiting {
		return ErrWaitlistFull
	}
	var dup int
	in.rdb.QueryRow(`SELECT COUNT(*) FROM waitlist WHERE name=? OR lower(contact)=lower(?)`, name, contact).Scan(&dup)
	if dup > 0 {
		return ErrOnList
	}
	if _, err := in.db.Exec(`INSERT INTO waitlist(name,contact,links,note,created) VALUES(?,?,?,?,?)`,
		name, contact, links, note, time.Now().Unix()); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrOnList
		}
		return err
	}
	return nil
}

// Waitlist lists the requests, oldest first.
func (in *Instance) Waitlist() ([]Waiting, error) {
	rows, err := in.rdb.Query(`SELECT id,name,contact,links,note,created,reviewed FROM waitlist ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Waiting
	for rows.Next() {
		var w Waiting
		var t int64
		if err := rows.Scan(&w.ID, &w.Name, &w.Contact, &w.Links, &w.Note, &t, &w.Reviewed); err != nil {
			return nil, err
		}
		w.Created = time.Unix(t, 0)
		out = append(out, w)
	}
	return out, rows.Err()
}

// SetReviewed marks a request as read, or as not read.
func (in *Instance) SetReviewed(id int64, reviewed bool) error {
	res, err := in.db.Exec(`UPDATE waitlist SET reviewed=? WHERE id=?`, reviewed, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: nobody with number %d is waiting", ErrNotFound, id)
	}
	return nil
}

// RemoveWaiting takes a request off the list: it was answered, or refused.
func (in *Instance) RemoveWaiting(id int64) error {
	res, err := in.db.Exec(`DELETE FROM waitlist WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: nobody with number %d is waiting", ErrNotFound, id)
	}
	return nil
}

// Counts reports how many accounts are served here and how many people wait.
func (in *Instance) Counts() (accounts, waiting int, err error) {
	if err = in.rdb.QueryRow(`SELECT COUNT(*) FROM users WHERE status!='moved'`).Scan(&accounts); err != nil {
		return
	}
	err = in.rdb.QueryRow(`SELECT COUNT(*) FROM waitlist`).Scan(&waiting)
	return
}
