// Command ttc-agent runs an AI participant for one textcast user, through the public API only.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/hbroek/text-thread-casting/internal/agent"
)

// loadEnv reads KEY=VALUE lines from path into the environment, without overriding variables already set.
func loadEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if ok && k != "" && !strings.HasPrefix(k, "#") && os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func main() {
	loadEnv(".env")
	base := flag.String("base", "", "the agent user's instance, e.g. http://dana.localhost:8080")
	token := flag.String("token", os.Getenv("TTC_TOKEN"), "API token (or TTC_TOKEN)")
	mode := flag.String("brain", "claude", "claude or stub")
	model := flag.String("model", "claude-haiku-5-5", "Claude model")
	persona := flag.String("persona", "Claude", "name the agent writes as")
	style := flag.String("style", "", `the agent's voice, e.g. "dry and skeptical" (default: friendly)`)
	every := flag.Duration("every", time.Minute, "time between cycles")
	once := flag.Bool("once", false, "run one cycle and exit")
	postEvery := flag.Int("post-every", 0, "write an original note every N cycles (0 = never)")
	max := flag.Int("max", 20, "most replies and posts over the agent's lifetime")
	state := flag.String("state", "ttc-agent.json", "file remembering what was handled")
	flag.Parse()
	if *base == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "usage: ttc-agent -base URL -token TOKEN [-brain claude|stub] [-once] ...")
		os.Exit(2)
	}
	var b agent.Brain = &agent.Stub{}
	if *mode == "claude" {
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is not set (use -brain stub to test without it)")
			os.Exit(2)
		}
		b = agent.NewClaude(key, *model)
	}
	a := agent.New(agent.NewClient(*base, *token), b, agent.Config{Persona: *persona, Style: *style, MaxReplies: *max, PostEvery: *postEvery, StatePath: *state})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	for {
		n, err := a.RunCycle(ctx)
		fmt.Printf("%s cycle: %d actions, err=%v\n", time.Now().Format(time.TimeOnly), n, err)
		if *once || ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(*every):
		}
	}
}
