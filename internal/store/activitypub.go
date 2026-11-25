package store

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"time"
)

// APAccount is what an account needs to take part in ActivityPub: where its actor lives and the key that proves it.
// The actor address is pinned when the account is first switched on and never changes, because it is the identity that
// followers on other servers hold.
type APAccount struct {
	Enabled    bool
	Actor      string // the actor's id: the account's origin and "/"
	HandleHost string // the domain of the handle the account is known by, if not the actor's own host
	PublicPEM  string
	PrivatePEM string // PKCS #8; empty only in a file that was exported and imported, and then made again by APAccount
	Created    time.Time
}

// APAccount returns the account's ActivityPub identity, or ErrNotFound if it was never switched on.
func (s *Space) APAccount(userID int64) (*APAccount, error) {
	a := &APAccount{}
	var en int
	var created int64
	err := s.db.QueryRow(`SELECT enabled,actor,public_pem,private_pem,created,handle_host FROM ap_account WHERE user_id=?`, userID).
		Scan(&en, &a.Actor, &a.PublicPEM, &a.PrivatePEM, &created, &a.HandleHost)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Enabled, a.Created = en != 0, time.Unix(created, 0)
	if a.PrivatePEM == "" { // the key was left out of an export; this copy gets its own
		if a.PublicPEM, a.PrivatePEM, err = newKeyPair(); err != nil {
			return nil, err
		}
		if _, err := s.db.Exec(`UPDATE ap_account SET public_pem=?,private_pem=? WHERE user_id=?`, a.PublicPEM, a.PrivatePEM, userID); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// APEnable switches ActivityPub on. The first time it makes the key and pins actor; later it only turns the account
// back on, with the identity it had.
func (s *Space) APEnable(userID int64, actor string) (*APAccount, error) {
	if _, err := s.APAccount(userID); errors.Is(err, ErrNotFound) {
		pub, priv, err := newKeyPair()
		if err != nil {
			return nil, err
		}
		if _, err := s.db.Exec(`INSERT INTO ap_account(user_id,enabled,actor,public_pem,private_pem,created) VALUES(?,1,?,?,?,?)`,
			userID, actor, pub, priv, time.Now().Unix()); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if _, err := s.db.Exec(`UPDATE ap_account SET enabled=1 WHERE user_id=?`, userID); err != nil {
		return nil, err
	}
	return s.APAccount(userID)
}

// APSetHandleHost sets the domain of the account's handle ("" for the actor's own host). It is a host name without a
// port; whether that domain points at the actor is up to the owner of the domain.
func (s *Space) APSetHandleHost(userID int64, host string) error {
	if host != "" {
		var err error
		if host, err = CleanDomain(host); err != nil {
			return err
		}
	}
	res, err := s.db.Exec(`UPDATE ap_account SET handle_host=? WHERE user_id=?`, host, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// APDisable switches ActivityPub off and keeps the identity.
func (s *Space) APDisable(userID int64) error {
	_, err := s.db.Exec(`UPDATE ap_account SET enabled=0 WHERE user_id=?`, userID)
	return err
}

// PostCount is how many posts the user has that are not removed.
func (s *Space) PostCount(userID int64) (n int) {
	s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE user_id=? AND deleted=0`, userID).Scan(&n)
	return n
}

// newKeyPair makes an RSA key of 2048 bits, the size and kind every ActivityPub server can check, as PEM.
func newKeyPair() (public, private string, err error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", "", err
	}
	pub, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// APActor is an actor on another server, as last fetched.
type APActor struct {
	ID, KeyID, PublicPEM string
	Inbox, SharedInbox   string
	Username, Name, Icon string
	Fetched              time.Time
}

// APActorByID returns the cached actor, or ErrNotFound.
func (in *Instance) APActorByID(id string) (*APActor, error) {
	return in.apActor(`id=?`, id)
}

// APActorByKey returns the cached actor that has this key id, or ErrNotFound.
func (in *Instance) APActorByKey(keyID string) (*APActor, error) {
	return in.apActor(`key_id=?`, keyID)
}

func (in *Instance) apActor(where, arg string) (*APActor, error) {
	a := &APActor{}
	var fetched int64
	err := in.db.QueryRow(`SELECT id,key_id,public_pem,inbox,shared_inbox,username,name,icon,fetched FROM ap_actors WHERE `+where+` LIMIT 1`, arg).
		Scan(&a.ID, &a.KeyID, &a.PublicPEM, &a.Inbox, &a.SharedInbox, &a.Username, &a.Name, &a.Icon, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	a.Fetched = time.Unix(fetched, 0)
	return a, err
}

// PutAPActor stores or replaces an actor.
func (in *Instance) PutAPActor(a *APActor) error {
	_, err := in.db.Exec(`INSERT INTO ap_actors(id,key_id,public_pem,inbox,shared_inbox,username,name,icon,fetched) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET key_id=excluded.key_id,public_pem=excluded.public_pem,inbox=excluded.inbox,shared_inbox=excluded.shared_inbox,
  username=excluded.username,name=excluded.name,icon=excluded.icon,fetched=excluded.fetched`,
		a.ID, a.KeyID, a.PublicPEM, a.Inbox, a.SharedInbox, a.Username, a.Name, a.Icon, time.Now().Unix())
	return err
}

// APFollower is someone on the fediverse who follows the account.
type APFollower struct {
	Actor, FollowID    string
	Inbox, SharedInbox string
	Handle, Name       string
	Since              time.Time
}

// APAddFollower records a follower, or refreshes what is known of one that already follows. It reports whether the
// follower is new.
func (s *Space) APAddFollower(userID int64, f APFollower) (isNew bool, err error) {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM ap_followers WHERE user_id=? AND actor=?`, userID, f.Actor).Scan(&n)
	_, err = s.db.Exec(`INSERT INTO ap_followers(user_id,actor,follow_id,inbox,shared_inbox,handle,name,since) VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(user_id,actor) DO UPDATE SET follow_id=excluded.follow_id,inbox=excluded.inbox,shared_inbox=excluded.shared_inbox,handle=excluded.handle,name=excluded.name`,
		userID, f.Actor, f.FollowID, f.Inbox, f.SharedInbox, f.Handle, f.Name, time.Now().Unix())
	return n == 0, err
}

// APRemoveFollower removes the follower with this actor address, or whose follow activity has this id (give "" for
// the one you do not know). It reports whether there was one.
func (s *Space) APRemoveFollower(userID int64, actor, followID string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM ap_followers WHERE user_id=? AND ((?<>'' AND actor=?) OR (?<>'' AND follow_id=?))`, userID, actor, actor, followID, followID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// APFollowers lists the followers, the newest first, at most limit of them (0: all).
func (s *Space) APFollowers(userID int64, limit int) ([]APFollower, error) {
	q := `SELECT actor,follow_id,inbox,shared_inbox,handle,name,since FROM ap_followers WHERE user_id=? ORDER BY since DESC, actor`
	args := []any{userID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APFollower
	for rows.Next() {
		var f APFollower
		var since int64
		if err := rows.Scan(&f.Actor, &f.FollowID, &f.Inbox, &f.SharedInbox, &f.Handle, &f.Name, &since); err != nil {
			return nil, err
		}
		f.Since = time.Unix(since, 0)
		out = append(out, f)
	}
	return out, rows.Err()
}

// APFollowerCount is how many followers the account has.
func (s *Space) APFollowerCount(userID int64) (n int) {
	s.db.QueryRow(`SELECT COUNT(*) FROM ap_followers WHERE user_id=?`, userID).Scan(&n)
	return n
}
