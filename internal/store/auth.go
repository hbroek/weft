package store

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// pbkdf2Iterations is a var so tests can lower it.
var pbkdf2Iterations = 600000

const SessionTTL = 30 * 24 * time.Hour

var ErrBadLogin = errors.New("invalid username or password")

func hashPassword(pw string, salt []byte) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iterations, 32)
}

// SetPassword sets a user's login password.
func (s *Store) SetPassword(name, pw string) error {
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	h, err := hashPassword(pw, salt)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE users SET pw_salt=?,pw_hash=? WHERE name=?`, salt, h, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CheckPassword verifies a login. It does the same work whether or not the user exists.
func (s *Store) CheckPassword(name, pw string) (*User, error) {
	var salt, want []byte
	u := &User{}
	err := s.db.QueryRow(`SELECT id,name,display,bio,pw_salt,pw_hash FROM users WHERE name=?`, name).
		Scan(&u.ID, &u.Name, &u.Display, &u.Bio, &salt, &want)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if len(salt) == 0 {
		salt, want = make([]byte, 16), make([]byte, 32)
	}
	got, err := hashPassword(pw, salt)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(got, want) != 1 || u.ID == 0 {
		return nil, ErrBadLogin
	}
	return u, nil
}

// CreateSession returns a new session token for the user.
func (s *Store) CreateSession(userID int64) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash,user_id,expires) VALUES(?,?,?)`,
		hashToken(tok), userID, time.Now().Add(SessionTTL).Unix())
	return tok, err
}

func (s *Store) UserBySession(tok string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT u.id,u.name,u.display,u.bio FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires>?`, hashToken(tok), time.Now().Unix()).Scan(&u.ID, &u.Name, &u.Display, &u.Bio)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) DeleteSession(tok string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(tok))
	return err
}
