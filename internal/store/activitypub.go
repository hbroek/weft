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
