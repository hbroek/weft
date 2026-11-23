package web

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hbroek/weft/internal/store"
)

// Moving an account between servers, with OAuth off or on at either end. Apps live in each server's own instance
// database and are tied to that server's address, so a move never carries them: the old server drops them, the new one
// has only what was connected to it, and an app has to be connected again at the new address. What the account's own
// token, password and posts do is the same whatever the flags.
func TestMoveBetweenServersWithAndWithoutOAuth(t *testing.T) {
	for _, oldOn := range []bool{false, true} {
		for _, newOn := range []bool{false, true} {
			t.Run(fmt.Sprintf("old=%v new=%v", oldOn, newOn), func(t *testing.T) {
				ts := setupTransfer(t)
				ts.old.s.cfg.OAuth, ts.nw.s.cfg.OAuth = oldOn, newOn

				// An app is connected on each server. They are made in the stores, so that they exist even where the
				// flag is off now (it was on when they were connected).
				oldApp, _ := ts.old.s.st.AddOAuthClient(store.OAuthClient{Name: "Quill", RedirectURIs: []string{"https://app.example/cb"}})
				newApp, _ := ts.nw.s.st.AddOAuthClient(store.OAuthClient{Name: "Other", RedirectURIs: []string{"https://other.example/cb"}})
				oldTok := tokenWith(t, ts.old.s, oldApp, "alice", "read post")
				newTok := tokenWith(t, ts.nw.s, newApp, "alice", "read")
				ts.old.post(t, `{"title":"Before","markdown":"Written before the move."}`)

				ts.receive(t, ts.code(t))
				if v := ts.wait(t); v.Err != "" || !v.Done {
					t.Fatalf("move: %+v", v)
				}

				// the old server: the apps are gone from it, and its address no longer serves the account
				if g, _ := ts.old.s.st.OAuthGrants("alice"); len(g) != 0 {
					t.Errorf("old server still lists %d connected apps", len(g))
				}
				if _, err := ts.old.s.st.OAuthAccess(oldTok); err == nil {
					t.Error("the old server still honours the app's token")
				}
				if w := do(ts.old.s, "GET", ts.old.host, "/api/posts", oldTok, ""); w.Code == 200 {
					t.Error("the old address answers the app")
				}

				// the new server: nothing came with the account
				if _, err := ts.nw.s.st.OAuthClient(oldApp.ID); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("the old server's app is known on the new one: %v", err)
				}
				if w := do(ts.nw.s, "GET", ts.nw.host, "/api/posts", oldTok, ""); w.Code != 401 {
					t.Errorf("the token of the old server on the new one: %d", w.Code)
				}
				sp, release, _ := ts.nw.s.st.Space("alice")
				var n int
				sp.RawDBForTests().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'oauth%'`).Scan(&n)
				release()
				if n != 0 {
					t.Errorf("the account's file holds %d OAuth tables", n)
				}

				// what was connected to the new server before the move is the new server's own business: it keeps
				// working if OAuth is on there, and is refused if it is off
				want := 401
				if newOn {
					want = 200
				}
				if w := do(ts.nw.s, "GET", ts.nw.host, "/api/posts", newTok, ""); w.Code != want {
					t.Errorf("an app connected to the new server before the move: %d, want %d", w.Code, want)
				}

				// the account's own token and the moved posts work on the new server either way
				if w := do(ts.nw.s, "GET", ts.nw.host, "/api/posts", ts.nw.tok, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Before") {
					t.Errorf("the account's token after the move: %d %s", w.Code, w.Body)
				}

				// an app is connected again at the new address, if the new server allows apps
				if newOn {
					fresh, _ := ts.nw.s.st.AddOAuthClient(store.OAuthClient{Name: "Quill", RedirectURIs: []string{"https://app.example/cb"}})
					tok := tokenWith(t, ts.nw.s, fresh, "alice", "read post")
					if w := do(ts.nw.s, "GET", ts.nw.host, "/api/posts", tok, ""); w.Code != 200 {
						t.Errorf("an app connected after the move: %d", w.Code)
					}
					if w := do(ts.nw.s, "GET", ts.nw.host, "/.well-known/oauth-authorization-server", "", ""); w.Code != 200 {
						t.Errorf("discovery on the new server: %d", w.Code)
					}
				} else if w := do(ts.nw.s, "GET", ts.nw.host, "/.well-known/oauth-authorization-server", "", ""); w.Code != 404 {
					t.Errorf("discovery on a server with OAuth off: %d", w.Code)
				}
			})
		}
	}
}
