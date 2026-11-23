package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/oauth"
)

func oauthInstance(t *testing.T) *Instance {
	t.Helper()
	in, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	for _, n := range []string{"alice", "bob"} {
		if _, err := in.AddUser(n, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	return in
}

func newClient(t *testing.T, in *Instance, name string) *OAuthClient {
	t.Helper()
	c, err := in.AddOAuthClient(OAuthClient{Name: name, RedirectURIs: []string{"https://app.example/cb", "http://127.0.0.1/cb"}, ClientURI: "https://app.example/"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// connect does what a user and an app do together: a grant, a code, and the swap of the code for tokens.
func connect(t *testing.T, in *Instance, c *OAuthClient, user, scope string) *OAuthTokens {
	t.Helper()
	g, err := in.CreateOAuthGrant(c.ID, user, scope)
	if err != nil {
		t.Fatal(err)
	}
	code, err := in.NewOAuthCode(g, "https://app.example/cb", strings.Repeat("c", 43))
	if err != nil {
		t.Fatal(err)
	}
	got, err := in.RedeemOAuthCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if got.GrantID != g || got.ClientID != c.ID || got.User != user || got.Scope != scope || got.RedirectURI != "https://app.example/cb" || got.Challenge != strings.Repeat("c", 43) {
		t.Fatalf("redeemed: %+v", got)
	}
	tk, err := in.IssueOAuthTokens(g)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestOAuthClients(t *testing.T) {
	in := oauthInstance(t)
	c := newClient(t, in, "Editor")
	got, err := in.OAuthClient(c.ID)
	if err != nil || got.Name != "Editor" || !reflect.DeepEqual(got.RedirectURIs, []string{"https://app.example/cb", "http://127.0.0.1/cb"}) || got.ClientURI != "https://app.example/" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := in.OAuthClient("wc_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown client: %v", err)
	}
	for _, bad := range []OAuthClient{
		{Name: "", RedirectURIs: []string{"https://a/"}},
		{Name: "x"},
		{Name: "x", RedirectURIs: []string{"https://a/\nhttps://evil/"}},
		{Name: "x", RedirectURIs: make([]string, oauth.MaxRedirects+1)},
	} {
		if _, err := in.AddOAuthClient(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

func TestOAuthRegistrationIsCapped(t *testing.T) {
	in := oauthInstance(t)
	if _, err := in.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?) INSERT INTO oauth_clients(id,name,redirect_uris,created) SELECT 'x'||i,'x','https://a/',? FROM n`, MaxOAuthClients, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := in.AddOAuthClient(OAuthClient{Name: "one more", RedirectURIs: []string{"https://a/"}}); !errors.Is(err, ErrTooManyClients) {
		t.Errorf("over the cap: %v", err)
	}
}

func TestOAuthCodeWorksOnceAndOnlyBriefly(t *testing.T) {
	in := oauthInstance(t)
	c := newClient(t, in, "App")
	g, _ := in.CreateOAuthGrant(c.ID, "alice", "read")
	code, _ := in.NewOAuthCode(g, "https://app.example/cb", "ch")
	if _, err := in.RedeemOAuthCode(code[:len(code)-1] + "x"); !errors.Is(err, ErrCodeInvalid) {
		t.Errorf("wrong code: %v", err)
	}
	if _, err := in.RedeemOAuthCode(code); err != nil {
		t.Fatal(err)
	}
	tk, _ := in.IssueOAuthTokens(g)
	if _, err := in.OAuthAccess(tk.Access); err != nil {
		t.Fatalf("the first tokens do not work: %v", err)
	}
	// the code again: refused, and what it made is revoked, since someone else may hold a copy
	if _, err := in.RedeemOAuthCode(code); !errors.Is(err, ErrCodeInvalid) {
		t.Errorf("reused code: %v", err)
	}
	if _, err := in.OAuthAccess(tk.Access); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("tokens survived a reused code: %v", err)
	}
	if _, err := in.RefreshOAuthTokens(tk.Refresh, c.ID, "alice"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("refresh token survived a reused code: %v", err)
	}

	// an expired code
	g, _ = in.CreateOAuthGrant(c.ID, "alice", "read")
	code, _ = in.NewOAuthCode(g, "https://app.example/cb", "ch")
	in.db.Exec(`UPDATE oauth_codes SET expires=? WHERE grant_id=?`, time.Now().Add(-time.Second).Unix(), g)
	if _, err := in.RedeemOAuthCode(code); !errors.Is(err, ErrCodeInvalid) {
		t.Errorf("expired code: %v", err)
	}
	// only a hash is kept
	var n int
	in.db.QueryRow(`SELECT COUNT(*) FROM oauth_codes WHERE hash=?`, code).Scan(&n)
	if n != 0 {
		t.Error("a code is stored as it is")
	}
}

func TestOAuthAccessToken(t *testing.T) {
	in := oauthInstance(t)
	c := newClient(t, in, "App")
	tk := connect(t, in, c, "alice", "read post")
	if tk.ExpiresIn != 3600 || tk.Scope != "read post" || !strings.HasPrefix(tk.Access, "wat_") || !strings.HasPrefix(tk.Refresh, "wrt_") {
		t.Fatalf("%+v", tk)
	}
	a, err := in.OAuthAccess(tk.Access)
	if err != nil || a.User != "alice" || a.Scope != "read post" || a.ClientID != c.ID {
		t.Fatalf("%+v %v", a, err)
	}
	// the refresh token is not an access token, nor the reverse; nothing stored is a token
	if _, err := in.OAuthAccess(tk.Refresh); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("refresh token used as an access token: %v", err)
	}
	if _, err := in.OAuthAccess(hashToken(tk.Access)); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("the stored hash works as a token: %v", err)
	}
	if _, err := in.RefreshOAuthTokens(tk.Access, c.ID, "alice"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("access token used as a refresh token: %v", err)
	}
	// it expires
	in.db.Exec(`UPDATE oauth_tokens SET expires=? WHERE refresh=0`, time.Now().Add(-time.Second).Unix())
	if _, err := in.OAuthAccess(tk.Access); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expired access token: %v", err)
	}
}

func TestOAuthLastUsedIsWrittenSeldom(t *testing.T) {
	in := oauthInstance(t)
	tk := connect(t, in, newClient(t, in, "App"), "alice", "read")
	in.OAuthAccess(tk.Access)
	var first int64
	in.db.QueryRow(`SELECT last_used FROM oauth_grants`).Scan(&first)
	if first == 0 {
		t.Fatal("last use not recorded")
	}
	in.db.Exec(`UPDATE oauth_grants SET last_used=last_used-1`)
	in.OAuthAccess(tk.Access)
	var second int64
	in.db.QueryRow(`SELECT last_used FROM oauth_grants`).Scan(&second)
	if second != first-1 {
		t.Errorf("last use written again within the interval: %d then %d", first-1, second)
	}
}

func TestOAuthRefreshRotates(t *testing.T) {
	in := oauthInstance(t)
	c := newClient(t, in, "App")
	other := newClient(t, in, "Other")
	t1 := connect(t, in, c, "alice", "read")

	if _, err := in.RefreshOAuthTokens(t1.Refresh, other.ID, "alice"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("another app's refresh: %v", err)
	}
	if _, err := in.OAuthAccess(t1.Access); err != nil {
		t.Fatalf("another app's try ended the grant: %v", err)
	}
	t2, err := in.RefreshOAuthTokens(t1.Refresh, c.ID, "alice")
	if err != nil || t2.Access == t1.Access || t2.Refresh == t1.Refresh || t2.Scope != "read" {
		t.Fatalf("%+v %v", t2, err)
	}
	if _, err := in.OAuthAccess(t1.Access); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("the old access token still works: %v", err)
	}
	if _, err := in.OAuthAccess(t2.Access); err != nil {
		t.Errorf("the new access token does not work: %v", err)
	}
	// the old refresh token, at once: a retry whose answer got lost; refused, but the grant stays
	if _, err := in.RefreshOAuthTokens(t1.Refresh, c.ID, "alice"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("old refresh token: %v", err)
	}
	if _, err := in.OAuthAccess(t2.Access); err != nil {
		t.Errorf("a quick retry ended the grant: %v", err)
	}
	// the old refresh token later: it was stolen or kept; everything of the grant goes
	in.db.Exec(`UPDATE oauth_tokens SET used=used-60 WHERE used<>0`)
	if _, err := in.RefreshOAuthTokens(t1.Refresh, c.ID, "alice"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("reused refresh token: %v", err)
	}
	if _, err := in.OAuthAccess(t2.Access); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("the grant survived a reused refresh token: %v", err)
	}
	if _, err := in.RefreshOAuthTokens(t2.Refresh, c.ID, "alice"); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("the new refresh token survived: %v", err)
	}
}

func TestOAuthRevoking(t *testing.T) {
	in := oauthInstance(t)
	c := newClient(t, in, "App")
	other := newClient(t, in, "Other")

	a := connect(t, in, c, "alice", "read")
	b := connect(t, in, other, "alice", "read")
	bob := connect(t, in, c, "bob", "read")

	// by token, only by the app it was made for; an unknown token is not an error
	in.RevokeOAuthToken(a.Access, other.ID)
	if _, err := in.OAuthAccess(a.Access); err != nil {
		t.Errorf("another app revoked it: %v", err)
	}
	if err := in.RevokeOAuthToken("wat_unknown", c.ID); err != nil {
		t.Error(err)
	}
	if err := in.RevokeOAuthToken(a.Refresh, c.ID); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"access": a.Access} {
		if _, err := in.OAuthAccess(tok); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("%s token after revoking its refresh token: %v", name, err)
		}
	}

	// by the user, who can only end their own
	gs, err := in.OAuthGrants("alice")
	if err != nil || len(gs) != 1 || gs[0].ClientName != "Other" || gs[0].User != "alice" || gs[0].Scope != "read" {
		t.Fatalf("grants: %+v %v", gs, err)
	}
	if err := in.RevokeOAuthGrant("bob", gs[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob ended alice's grant: %v", err)
	}
	if err := in.RevokeOAuthGrant("alice", gs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := in.OAuthAccess(b.Access); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("token after the grant ended: %v", err)
	}
	if _, err := in.OAuthAccess(bob.Access); err != nil {
		t.Errorf("bob's token: %v", err)
	}
	// everything of one account
	connect(t, in, c, "alice", "read")
	if err := in.RevokeOAuthGrants("alice"); err != nil {
		t.Fatal(err)
	}
	if gs, _ := in.OAuthGrants("alice"); len(gs) != 0 {
		t.Errorf("grants left: %+v", gs)
	}
	if _, err := in.OAuthAccess(bob.Access); err != nil {
		t.Errorf("alice's revocation reached bob: %v", err)
	}
}

func TestOAuthConnectingAgainReplacesTheOldGrant(t *testing.T) {
	in := oauthInstance(t)
	c := newClient(t, in, "App")
	old := connect(t, in, c, "alice", "read")
	// a grant that is not finished does not touch the working one
	g, _ := in.CreateOAuthGrant(c.ID, "alice", "read post")
	if _, err := in.OAuthAccess(old.Access); err != nil {
		t.Fatalf("an unfinished grant ended the old one: %v", err)
	}
	if gs, _ := in.OAuthGrants("alice"); len(gs) != 1 {
		t.Errorf("an unfinished grant is listed: %+v", gs)
	}
	code, _ := in.NewOAuthCode(g, "https://app.example/cb", "ch")
	if _, err := in.RedeemOAuthCode(code); err != nil {
		t.Fatal(err)
	}
	// a code that is redeemed but fails the PKCE check is never followed by tokens, and must not end the old grant
	if _, err := in.OAuthAccess(old.Access); err != nil {
		t.Fatalf("redeeming a code ended the old grant: %v", err)
	}
	if _, err := in.IssueOAuthTokens(g); err != nil {
		t.Fatal(err)
	}
	if _, err := in.OAuthAccess(old.Access); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("the old grant survived the new one: %v", err)
	}
	if gs, _ := in.OAuthGrants("alice"); len(gs) != 1 || gs[0].Scope != "read post" {
		t.Errorf("grants: %+v", gs)
	}
}

func TestOAuthEndsWhenTheAccountLeaves(t *testing.T) {
	in := oauthInstance(t)
	c := newClient(t, in, "App")
	removed := connect(t, in, c, "alice", "read")
	kept := connect(t, in, c, "bob", "read")
	if err := in.Remove("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.OAuthAccess(removed.Access); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("a removed account's token: %v", err)
	}
	if err := in.MarkMoved("bob", "https://new.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.OAuthAccess(kept.Access); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("a moved account's token: %v", err)
	}
}

func TestOAuthGrantsPerAccountAreCapped(t *testing.T) {
	in := oauthInstance(t)
	c := newClient(t, in, "App")
	for i := 0; i < MaxOAuthGrants; i++ {
		if _, err := in.CreateOAuthGrant(c.ID, "alice", "read"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := in.CreateOAuthGrant(c.ID, "alice", "read"); !errors.Is(err, ErrTooManyGrants) {
		t.Errorf("over the cap: %v", err)
	}
	if _, err := in.CreateOAuthGrant(c.ID, "bob", "read"); err != nil {
		t.Errorf("another account is affected: %v", err)
	}
}

func TestOAuthPrune(t *testing.T) {
	in := oauthInstance(t)
	c := newClient(t, in, "Used")
	unused := newClient(t, in, "Never connected")
	live := connect(t, in, c, "alice", "read")
	// a grant nobody finished, and old
	stale, _ := in.CreateOAuthGrant(c.ID, "bob", "read")
	in.db.Exec(`UPDATE oauth_grants SET created=created-3600 WHERE id=?`, stale)
	// a young one that has not got its code yet is left alone
	young, _ := in.CreateOAuthGrant(c.ID, "bob", "read")
	in.db.Exec(`UPDATE oauth_clients SET created=created-?`, int64(OAuthClientTTL.Seconds())+60)

	if err := in.PruneOAuth(time.Now()); err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) (n int) { in.db.QueryRow(q, args...).Scan(&n); return }
	if count(`SELECT COUNT(*) FROM oauth_grants WHERE id=?`, stale) != 0 || count(`SELECT COUNT(*) FROM oauth_grants WHERE id=?`, young) != 1 {
		t.Error("unfinished grants")
	}
	if _, err := in.OAuthClient(unused.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a client nobody connected is kept: %v", err)
	}
	if _, err := in.OAuthClient(c.ID); err != nil {
		t.Errorf("a client with a grant was forgotten: %v", err)
	}
	if _, err := in.OAuthAccess(live.Access); err != nil {
		t.Errorf("a live token was pruned: %v", err)
	}
	// a token that has expired goes; with all of a grant's tokens gone, the grant goes too
	in.db.Exec(`UPDATE oauth_tokens SET expires=?`, time.Now().Add(-time.Hour).Unix())
	in.db.Exec(`UPDATE oauth_grants SET created=created-3600`) // and past the grace for a grant just made
	if err := in.PruneOAuth(time.Now()); err != nil {
		t.Fatal(err)
	}
	if count(`SELECT COUNT(*) FROM oauth_tokens`) != 0 || count(`SELECT COUNT(*) FROM oauth_grants WHERE name='alice'`) != 0 {
		t.Error("expired tokens or their grant remain")
	}
}

// A server that never uses OAuth pays one thing: empty tables. Nothing here touches a user's own file.
func TestOAuthLeavesUserFilesAlone(t *testing.T) {
	if strings.Contains(spaceSchema+lazySchema, "oauth") {
		t.Error("OAuth data must live in instance.db, not in users' files")
	}
}

// One source filling the table with apps nobody connects must not keep real apps out; an app somebody connected stays.
func TestFullTableDropsTheOldestUnusedApp(t *testing.T) {
	in := oauthInstance(t)
	mk := func() *OAuthClient {
		c, err := in.AddOAuthClient(OAuthClient{Name: "x", RedirectURIs: []string{"https://a.example/cb"}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first := mk()
	if _, err := in.CreateOAuthGrant(first.ID, "alice", "read"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxOAuthClients; i++ {
		mk()
	}
	if _, err := in.AddOAuthClient(OAuthClient{Name: "y", RedirectURIs: []string{"https://a.example/cb"}}); !errors.Is(err, ErrTooManyClients) {
		t.Fatalf("full table, all new: %v", err)
	}
	if _, err := in.db.Exec(`UPDATE oauth_clients SET created=created-3600`); err != nil {
		t.Fatal(err)
	}
	if _, err := in.AddOAuthClient(OAuthClient{Name: "y", RedirectURIs: []string{"https://a.example/cb"}}); err != nil {
		t.Fatalf("old unused apps should make room: %v", err)
	}
	if _, err := in.OAuthClient(first.ID); err != nil {
		t.Errorf("the connected app was dropped: %v", err)
	}
	var n int
	in.db.QueryRow(`SELECT COUNT(*) FROM oauth_clients`).Scan(&n)
	if n != MaxOAuthClients {
		t.Errorf("%d apps", n)
	}
}
