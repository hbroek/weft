package store

import (
	"database/sql"
	"errors"
	"sort"
	"time"
)

// Reaction polarities. Every reaction has one; the emoji adds nuance.
const (
	Plus  = "+"
	Minus = "-"
)

// DefaultEmoji is shown for a reaction published without an emoji.
func DefaultEmoji(polarity string) string {
	if polarity == Minus {
		return "👎"
	}
	return "👍"
}

// ---- Reactions the user has published ----

type Reaction struct {
	ID, UserID         int64
	Target, TargetFeed string
	Polarity, Emoji    string
	Created            time.Time
	Updated            *time.Time
	Deleted            bool
}

const reactionCols = `id,user_id,target,target_feed,polarity,emoji,created,updated,deleted`

func scanReaction(r interface{ Scan(...any) error }) (*Reaction, error) {
	x := &Reaction{}
	var created int64
	var upd *int64
	var del int
	if err := r.Scan(&x.ID, &x.UserID, &x.Target, &x.TargetFeed, &x.Polarity, &x.Emoji, &created, &upd, &del); err != nil {
		return nil, err
	}
	x.Created = time.Unix(created, 0)
	if upd != nil {
		t := time.Unix(*upd, 0)
		x.Updated = &t
	}
	x.Deleted = del != 0
	return x, nil
}

// SetReaction publishes a reaction to target, or changes the user's existing one in place.
func (s *Store) SetReaction(userID int64, target, targetFeed, polarity, emoji string) (*Reaction, error) {
	if polarity != Plus && polarity != Minus {
		return nil, ErrInvalid
	}
	if old, err := s.ActiveReaction(userID, target); err == nil {
		if _, err := s.db.Exec(`UPDATE reactions SET polarity=?,emoji=?,target_feed=CASE WHEN ?<>'' THEN ? ELSE target_feed END,updated=? WHERE id=?`,
			polarity, emoji, targetFeed, targetFeed, time.Now().Unix(), old.ID); err != nil {
			return nil, err
		}
		return s.reactionByID(old.ID)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	res, err := s.db.Exec(`INSERT INTO reactions(user_id,target,target_feed,polarity,emoji,created) VALUES(?,?,?,?,?,?)`,
		userID, target, targetFeed, polarity, emoji, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.reactionByID(id)
}

func (s *Store) reactionByID(id int64) (*Reaction, error) {
	x, err := scanReaction(s.db.QueryRow(`SELECT `+reactionCols+` FROM reactions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// Reaction returns one of the user's reactions, including a removed one (for its tombstone).
func (s *Store) Reaction(userID, id int64) (*Reaction, error) {
	x, err := s.reactionByID(id)
	if err != nil {
		return nil, err
	}
	if x.UserID != userID {
		return nil, ErrNotFound
	}
	return x, nil
}

// ActiveReaction returns the user's current reaction to target.
func (s *Store) ActiveReaction(userID int64, target string) (*Reaction, error) {
	x, err := scanReaction(s.db.QueryRow(`SELECT `+reactionCols+` FROM reactions WHERE user_id=? AND target=? AND deleted=0`, userID, target))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// RemoveReaction withdraws the user's reaction to target. The row stays so its permalink can answer 410.
func (s *Store) RemoveReaction(userID int64, target string) (*Reaction, error) {
	x, err := s.ActiveReaction(userID, target)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`UPDATE reactions SET deleted=1, updated=? WHERE id=?`, time.Now().Unix(), x.ID); err != nil {
		return nil, err
	}
	return s.reactionByID(x.ID)
}

// ActiveReactions lists the user's current reactions, newest first.
func (s *Store) ActiveReactions(userID int64, limit int) ([]*Reaction, error) {
	rows, err := s.db.Query(`SELECT `+reactionCols+` FROM reactions WHERE user_id=? AND deleted=0 ORDER BY created DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Reaction
	for rows.Next() {
		x, err := scanReaction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ---- Reactions received by Webmention ----

type ReactionIn struct {
	ID, PostID              int64
	Source, FeedURL, Author string
	Polarity, Emoji         string
	Trusted                 bool
	Created                 time.Time
}

// UpsertReactionIn stores or refreshes a verified reaction. Emoji must already be resolved.
func (s *Store) UpsertReactionIn(r ReactionIn) error {
	_, err := s.db.Exec(`INSERT INTO reactions_in(post_id,source,feed_url,author,polarity,emoji,trusted,created) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(post_id,source) DO UPDATE SET feed_url=excluded.feed_url, author=excluded.author,
		  polarity=excluded.polarity, emoji=excluded.emoji, trusted=excluded.trusted`,
		r.PostID, r.Source, r.FeedURL, r.Author, r.Polarity, r.Emoji, b2i(r.Trusted), time.Now().Unix())
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) DeleteReactionIn(postID int64, source string) error {
	_, err := s.db.Exec(`DELETE FROM reactions_in WHERE post_id=? AND source=?`, postID, source)
	return err
}

// ReactionsInFor lists every verified reaction to a post, trusted or not.
func (s *Store) ReactionsInFor(postID int64) ([]ReactionIn, error) {
	rows, err := s.db.Query(`SELECT id,post_id,source,feed_url,author,polarity,emoji,trusted,created FROM reactions_in WHERE post_id=? ORDER BY created,id`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReactionIn
	for rows.Next() {
		var r ReactionIn
		var tr int
		var c int64
		if err := rows.Scan(&r.ID, &r.PostID, &r.Source, &r.FeedURL, &r.Author, &r.Polarity, &r.Emoji, &tr, &c); err != nil {
			return nil, err
		}
		r.Trusted, r.Created = tr != 0, time.Unix(c, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

type EmojiCount struct {
	Emoji string
	Count int
}

// Tally counts a post's reactions from trusted senders.
type Tally struct {
	Plus, Minus int
	Emoji       []EmojiCount // most used first
}

// Tallies returns the trusted-sender tally for each of the user's posts that has one.
func (s *Store) Tallies(userID int64) (map[int64]*Tally, error) {
	rows, err := s.db.Query(`SELECT r.post_id, r.polarity, r.emoji, COUNT(*) FROM reactions_in r JOIN posts p ON p.id=r.post_id
		WHERE p.user_id=? AND r.trusted=1 GROUP BY r.post_id, r.polarity, r.emoji`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*Tally{}
	for rows.Next() {
		var id int64
		var pol, em string
		var n int
		if err := rows.Scan(&id, &pol, &em, &n); err != nil {
			return nil, err
		}
		t := out[id]
		if t == nil {
			t = &Tally{}
			out[id] = t
		}
		if pol == Minus {
			t.Minus += n
		} else {
			t.Plus += n
		}
		t.Emoji = append(t.Emoji, EmojiCount{em, n})
	}
	for _, t := range out {
		sort.Slice(t.Emoji, func(i, j int) bool {
			if t.Emoji[i].Count != t.Emoji[j].Count {
				return t.Emoji[i].Count > t.Emoji[j].Count
			}
			return t.Emoji[i].Emoji < t.Emoji[j].Emoji
		})
	}
	return out, rows.Err()
}
