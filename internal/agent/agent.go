package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// maxAttempts is how many cycles the agent tries a post before giving up on it.
const maxAttempts = 3

// Config bounds what the agent may do, so two agents can never talk forever.
type Config struct {
	Persona    string // name the agent writes as
	Style      string // optional voice, e.g. "dry and skeptical"
	MaxReplies int    // total replies and posts over the agent's lifetime; 0 means 20
	PerCycle   int    // at most this many new replies per cycle; 0 means 5
	PostEvery  int    // write an original note every N cycles; 0 means never
	StatePath  string
	Log        *log.Logger
}

type state struct {
	Seen   map[string]bool `json:"seen"` // post links and reply sources already handled
	Fails  map[string]int  `json:"fails,omitempty"`
	Posted int             `json:"posted"`
	Cycles int             `json:"cycles"`
}

type Agent struct {
	C     *Client
	B     Brain
	Cfg   Config
	mu    sync.Mutex
	state state
	// answered holds the thread roots replied to during the current cycle.
	answered map[string]bool
}

func New(c *Client, b Brain, cfg Config) *Agent {
	if cfg.MaxReplies == 0 {
		cfg.MaxReplies = 20
	}
	if cfg.PerCycle == 0 {
		cfg.PerCycle = 5
	}
	if cfg.Persona == "" {
		cfg.Persona = "Claude"
	}
	if cfg.Log == nil {
		cfg.Log = log.New(os.Stderr, "agent: ", log.LstdFlags)
	}
	a := &Agent{C: c, B: b, Cfg: cfg, state: state{Seen: map[string]bool{}}}
	if cfg.StatePath != "" {
		if data, err := os.ReadFile(cfg.StatePath); err == nil {
			json.Unmarshal(data, &a.state)
			if a.state.Seen == nil {
				a.state.Seen = map[string]bool{}
			}
		}
	}
	return a
}

// seenKey identifies a post by URL and publication time, so that a reset database which hands out
// the same URLs again does not make the agent skip brand-new posts.
func seenKey(link string, published time.Time) string {
	return fmt.Sprintf("%s@%d", link, published.Unix())
}

func (a *Agent) persona() Persona { return Persona{Name: a.Cfg.Persona, Style: a.Cfg.Style} }

func (a *Agent) save() {
	if a.Cfg.StatePath == "" {
		return
	}
	data, _ := json.Marshal(a.state)
	os.WriteFile(a.Cfg.StatePath, data, 0o600)
}

// RunCycle handles what is new on the timeline and in replies to the agent's own posts, then
// maybe writes an original note. It returns the number of things it posted or reacted with.
func (a *Agent) RunCycle(ctx context.Context) (acted int, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	defer a.save()
	a.state.Cycles++
	a.answered = map[string]bool{}
	budget := a.Cfg.PerCycle

	tl, err := a.C.Timeline(ctx)
	if err != nil {
		return 0, err
	}
	for _, it := range tl {
		if budget == 0 || ctx.Err() != nil {
			break
		}
		if it.FeedURL == a.C.FeedURL() || a.state.Seen[seenKey(it.Link, it.Published)] {
			continue
		}
		a.state.Seen[seenKey(it.Link, it.Published)] = true
		n, err := a.handle(ctx, it, it.Link, it.FeedURL, seenKey(it.Link, it.Published), &budget)
		acted += n
		if err != nil {
			a.Cfg.Log.Printf("%s: %v", it.Link, err)
		}
	}

	// Replies to the agent's own posts, whether or not the author is approved: answer once each.
	for _, st := range []string{"approved", "pending"} {
		rs, err := a.C.Replies(ctx, st)
		if err != nil {
			a.Cfg.Log.Printf("replies: %v", err)
			continue
		}
		for _, r := range rs {
			if budget == 0 || a.state.Seen[seenKey(r.Source, r.Published)] {
				continue
			}
			key := seenKey(r.Source, r.Published)
			a.state.Seen[key] = true
			it := Item{FeedTitle: r.Author, Text: r.Text, Link: r.Source}
			n, err := a.handle(ctx, it, r.Source, "", key, &budget)
			acted += n
			if err != nil {
				a.Cfg.Log.Printf("%s: %v", r.Source, err)
			}
		}
	}

	if a.Cfg.PostEvery > 0 && a.state.Cycles%a.Cfg.PostEvery == 0 && a.state.Posted < a.Cfg.MaxReplies {
		title, md, err := a.B.Post(ctx, a.persona())
		if err == nil && md != "" {
			if _, err = a.C.Post(ctx, title, md, "", ""); err == nil {
				a.state.Posted++
				acted++
			}
		}
		if err != nil {
			a.Cfg.Log.Printf("post: %v", err)
		}
	}
	return acted, nil
}

func (a *Agent) handle(ctx context.Context, it Item, target, targetFeed, key string, budget *int) (acted int, err error) {
	// Never answer a post without knowing the conversation it belongs to. If the thread cannot be
	// read, leave the post unhandled so that the next cycle tries again.
	thread, err := a.C.Thread(ctx, target)
	if err != nil || len(thread) == 0 {
		delete(a.state.Seen, key)
		if err == nil {
			err = errors.New("empty thread")
		}
		return 0, err
	}
	d, err := a.B.Decide(ctx, a.persona(), it, thread)
	if err != nil {
		// A brain failure (rate limit, an unusable answer) should not lose the post for good:
		// try again next cycle, but give up after a few attempts.
		if a.state.Fails == nil {
			a.state.Fails = map[string]int{}
		}
		if a.state.Fails[key]++; a.state.Fails[key] < maxAttempts {
			delete(a.state.Seen, key)
		}
		return 0, err
	}
	// One reply per conversation per cycle: the first reply may already change the discussion.
	if root := thread[0].URL; a.answered[root] {
		d.Reply = ""
	} else if d.Reply != "" {
		a.answered[root] = true
	}
	// Reactions need a feed to be tallied against; replies to a reply may not know one.
	if d.Polarity != "" && targetFeed != "" {
		if err = a.C.React(ctx, target, targetFeed, d.Polarity, d.Emoji); err != nil {
			return acted, err
		}
		acted++
	}
	if d.Reply != "" && a.state.Posted < a.Cfg.MaxReplies {
		if _, err = a.C.Post(ctx, "", d.Reply, target, targetFeed); err != nil {
			return acted, err
		}
		a.state.Posted++
		*budget--
		acted++
	}
	return acted, nil
}
