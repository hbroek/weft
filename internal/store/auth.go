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

// MinPassword is the shortest password accepted.
const MinPassword = 8

// SetPassword sets the login password for good.
func (s *Space) SetPassword(pw string) error { return s.setPassword(pw, false) }

// SetTemporaryPassword sets a password the user must replace at the first sign-in: for an account an
// administrator made, or a password an administrator reset, since the administrator knows it.
func (s *Space) SetTemporaryPassword(pw string) error { return s.setPassword(pw, true) }

func (s *Space) setPassword(pw string, temporary bool) error {
	if len(pw) < MinPassword {
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
	res, err := s.db.Exec(`UPDATE users SET pw_salt=?,pw_hash=?,must_change=? WHERE name=?`, salt, h, b2i(temporary), s.name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CheckPassword verifies a login as this space's user.
func (s *Space) CheckPassword(pw string) (*User, error) {
	var salt, want []byte
	u := &User{}
	err := s.db.QueryRow(`SELECT id,name,display,bio,avatar,pw_salt,pw_hash FROM users WHERE name=?`, s.name).
		Scan(&u.ID, &u.Name, &u.Display, &u.Bio, &u.Avatar, &salt, &want)
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
func (s *Space) CreateSession(userID int64) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash,user_id,expires) VALUES(?,?,?)`,
		hashToken(tok), userID, time.Now().Add(SessionTTL).Unix())
	return tok, err
}

// UserBySession returns the space's user if tok is one of their live sessions.
func (s *Space) UserBySession(tok string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT u.id,u.name,u.display,u.bio,u.avatar FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires>? AND u.name=?`, hashToken(tok), time.Now().Unix(), s.name).Scan(&u.ID, &u.Name, &u.Display, &u.Bio, &u.Avatar)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Space) DeleteSession(tok string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(tok))
	return err
}

// MustChangePassword reports whether the user signed in, or would sign in, with a temporary password.
func (s *Space) MustChangePassword() bool {
	var n int
	s.db.QueryRow(`SELECT must_change FROM users WHERE name=?`, s.name).Scan(&n)
	return n != 0
}

var (
	ErrWeakPassword = errors.New("the new password must be at least 8 characters")
	ErrSamePassword = errors.New("the new password must be different from the old one")
)

// ChangePassword replaces the password after checking the old one. The new one is permanent. Every session but
// keep (the one making the change) is ended: whoever else was signed in with the old password is not any more.
func (s *Space) ChangePassword(current, next, keep string) error {
	if _, err := s.CheckPassword(current); err != nil {
		return err
	}
	if len(next) < MinPassword {
		return ErrWeakPassword
	}
	if next == current {
		return ErrSamePassword
	}
	if err := s.SetPassword(next); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash!=?`, hashToken(keep))
	return err
}

// passwordAlphabet leaves out what is easily mistaken for something else (l, 1, o, 0, i) and anything that
// would be read as a word, so a password can be read out or typed from a screen.
const passwordAlphabet = "bcdfghjkmnpqrstvwxz23456789"

// GeneratePassword returns a random password of two groups of five characters, such as "kx4mq-9bnrt", about 47
// bits: short enough to read out, and enough for a password that is replaced at the first sign-in.
func GeneratePassword() string {
	var b [10]byte
	rand.Read(b[:])
	out := make([]byte, 0, 11)
	for i, c := range b {
		if i > 0 && i%5 == 0 {
			out = append(out, '-')
		}
		out = append(out, passwordAlphabet[int(c)%len(passwordAlphabet)])
	}
	return string(out)
}
