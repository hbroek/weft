package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ---- Pictures the user has uploaded ----
//
// A picture is stored in the user's own file, under a name made of the hash of its bytes, so the same picture
// is kept once and its address never changes.

// ErrQuota means the picture does not fit in what the user may store.
var ErrQuota = errors.New("storage quota reached")

// Media is a stored picture. Data is empty in a listing.
type Media struct {
	Name, Mime    string // Name is "<hash>.<ext>"
	Size          int64
	Width, Height int
	Origin        string // where a copy was made from, if it was
	Created       time.Time
	Data          []byte
}

// PutMedia stores a picture unless one of the same name is already there. It reports whether it was added.
// quota is the most all the user's pictures may take together (0 for no limit).
func (s *Space) PutMedia(m Media, quota int64) (bool, error) {
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM media WHERE name=?`, m.Name).Scan(&one); err == nil {
		return false, nil
	}
	limit := quota
	if quota <= 0 {
		limit = 1 << 62
	}
	res, err := s.db.Exec(`INSERT INTO media(name,mime,size,width,height,origin,created,data)
		SELECT ?,?,?,?,?,?,?,? WHERE (SELECT COALESCE(SUM(size),0) FROM media) + ? <= ?`,
		m.Name, m.Mime, len(m.Data), m.Width, m.Height, m.Origin, time.Now().Unix(), m.Data, len(m.Data), limit)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, ErrQuota
	}
	return true, nil
}

// Media returns a stored picture with its bytes, or ErrNotFound.
func (s *Space) Media(name string) (*Media, error) {
	m := &Media{Name: name}
	var created int64
	err := s.db.QueryRow(`SELECT mime,size,width,height,origin,created,data FROM media WHERE name=?`, name).
		Scan(&m.Mime, &m.Size, &m.Width, &m.Height, &m.Origin, &created, &m.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Created = time.Unix(created, 0)
	return m, nil
}

// MediaList lists the user's pictures without their bytes, newest first.
func (s *Space) MediaList() ([]Media, error) {
	rows, err := s.db.Query(`SELECT name,mime,size,width,height,origin,created FROM media ORDER BY created DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Media
	for rows.Next() {
		var m Media
		var created int64
		if err := rows.Scan(&m.Name, &m.Mime, &m.Size, &m.Width, &m.Height, &m.Origin, &created); err != nil {
			return nil, err
		}
		m.Created = time.Unix(created, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// MediaUsed is how many bytes the user's pictures take.
func (s *Space) MediaUsed() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(size),0) FROM media`).Scan(&n)
	return n, err
}

// DeleteMedia removes a picture. Posts that show it will show a broken picture.
func (s *Space) DeleteMedia(name string) error {
	_, err := s.db.Exec(`DELETE FROM media WHERE name=?`, name)
	return err
}

// PostsUsingMedia counts the user's posts (not deleted) whose Markdown mentions the picture.
func (s *Space) PostsUsingMedia(name string) (int, error) {
	var n int
	like := "%/media/" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(name) + "%"
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE deleted=0 AND markdown LIKE ? ESCAPE '\'`, like).Scan(&n)
	return n, err
}

// MediaMeta returns a stored picture without its bytes, or ErrNotFound.
func (s *Space) MediaMeta(name string) (*Media, error) {
	m := &Media{Name: name}
	var created int64
	err := s.db.QueryRow(`SELECT mime,size,width,height,origin,created FROM media WHERE name=?`, name).
		Scan(&m.Mime, &m.Size, &m.Width, &m.Height, &m.Origin, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Created = time.Unix(created, 0)
	return m, nil
}
