package agent

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sync"
)

// Config bounds what the agent may do, so two agents can never talk forever.
type Config struct {
	Persona    string
	MaxReplies int // total replies and posts over the agent's lifetime; 0 means 20
	PerCycle   int // at most this many new replies per cycle; 0 means 5
	PostEvery  int // write an original note every N cycles; 0 means never
	StatePath  string
	Log        *log.Logger
}

type state struct {
	Seen   map[string]bool `json:"seen"` // post links and reply sources already handled
	Posted int             `json:"posted"`
	Cycles int             `json:"cycles"`
}

type Agent struct {
	C     *Client
	B     Brain
	Cfg   Config
	mu    sync.Mutex
	state state
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
	budget := a.Cfg.PerCycle

	tl, err := a.C.Timeline(ctx)
	if err != nil {
		return 0, err
	}
	for _, it := range tl {
		if budget == 0 || ctx.Err() != nil {
			break
		}
		if it.FeedURL == a.C.FeedURL() || a.state.Seen[it.Link] {
			continue
		}
		a.state.Seen[it.Link] = true
		n, err := a.handle(ctx, it, it.Link, it.FeedURL, &budget)
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
			if budget == 0 || a.state.Seen[r.Source] {
				continue
			}
			a.state.Seen[r.Source] = true
			it := Item{FeedTitle: r.Author, Text: r.Text, Link: r.Source}
			n, err := a.handle(ctx, it, r.Source, "", &budget)
			acted += n
			if err != nil {
				a.Cfg.Log.Printf("%s: %v", r.Source, err)
			}
		}
	}

	if a.Cfg.PostEvery > 0 && a.state.Cycles%a.Cfg.PostEvery == 0 && a.state.Posted < a.Cfg.MaxReplies {
		title, md, err := a.B.Post(ctx, a.Cfg.Persona)
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

func (a *Agent) handle(ctx context.Context, it Item, target, targetFeed string, budget *int) (acted int, err error) {
	d, err := a.B.Decide(ctx, a.Cfg.Persona, it)
	if err != nil {
		return 0, err
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
