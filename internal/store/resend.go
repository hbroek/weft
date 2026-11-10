package store

import (
	"os"
	"strconv"
)

// Outgoing is a page of an account that is sent as a Webmention: a reply, or a like, with the page it answers.
type Outgoing struct {
	Path   string // the page on the account's own site: /posts/<slug> or /likes/<id>
	Target string // the page it replies to or likes
}

// Outgoing lists every reply and like of the user that is still published, for sending the Webmentions again
// when the account has moved to a new address, or has been removed.
func (s *Space) Outgoing(userID int64) ([]Outgoing, error) {
	var out []Outgoing
	rows, err := s.db.Query(`SELECT slug,in_reply_to FROM posts WHERE user_id=? AND deleted=0 AND in_reply_to!='' ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var slug, target string
		if err := rows.Scan(&slug, &target); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, Outgoing{"/posts/" + slug, target})
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT id,target FROM likes WHERE user_id=? AND deleted=0 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var target string
		if err := rows.Scan(&id, &target); err != nil {
			return nil, err
		}
		out = append(out, Outgoing{"/likes/" + strconv.FormatInt(id, 10), target})
	}
	return out, rows.Err()
}

func retractedKey(name string) string { return "retracted:" + name }

// NeedingRetraction lists removed accounts whose file is still in place and whose replies and likes have not yet
// been retracted at the receivers.
func (in *Instance) NeedingRetraction() []string {
	rows, err := in.rdb.Query(`SELECT name FROM users WHERE status='removed'`)
	if err != nil {
		return nil
	}
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	rows.Close()
	var out []string
	for _, n := range names {
		if _, err := os.Stat(in.spacePath(n)); err == nil && in.Setting(retractedKey(n)) == "" {
			out = append(out, n)
		}
	}
	return out
}

// MarkRetracted records that the Webmentions of a removed account have been queued.
func (in *Instance) MarkRetracted(name string) error { return in.SetSetting(retractedKey(name), "1") }

// hasWork reports whether the user still has Webmention jobs waiting.
func (in *Instance) hasWork(name string) bool {
	var n int
	in.rdb.QueryRow(`SELECT COUNT(*) FROM work WHERE name=?`, name).Scan(&n)
	return n > 0
}
