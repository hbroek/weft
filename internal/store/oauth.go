package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/oauth"
)

// OAuth lets an app act for a user without the user's password or API token. An app (a client) registers; a user
// lets it do some things (a grant); the app swaps a one-time code for an access token and a refresh token. The
// rules about what is acceptable (redirect addresses, PKCE, scopes) are in package oauth; here is only storage. It
// is kept in instance.db, not in the users' files, which hold what the users wrote. Only hashes are stored.

var (
	ErrTooManyClients = errors.New("too many apps are registered on this server")
	ErrTooManyGrants  = errors.New("this account has too many apps connected")
	ErrCodeInvalid    = errors.New("the code is not valid")
	ErrTokenInvalid   = errors.New("the token is not valid")
)

const (
	// MaxOAuthClients is how many apps a server keeps. Registering is open to anyone, so it is capped; an app that
	// no one ever connected is forgotten after OAuthClientTTL.
	MaxOAuthClients = 2000
	// MaxOAuthGrants is how many apps one account may have connected, counting those it has not finished connecting.
	MaxOAuthGrants = 100

	OAuthCodeTTL    = 60 * time.Second
	OAuthAccessTTL  = time.Hour
	OAuthRefreshTTL = 30 * 24 * time.Hour
	OAuthClientTTL  = 7 * 24 * time.Hour

	// oauthRetry is how long after a refresh token was used that presenting it again is taken for a retry of the same
	// call (the answer got lost) and not for theft: it fails, but the grant stays.
	oauthRetry = 10 * time.Second
	// oauthLastUsedEvery limits how often a grant's "last used" time is written, so reading is not writing.
	oauthLastUsedEvery = 5 * time.Minute
)

// OAuthClient is an app that registered.
type OAuthClient struct {
	ID, Name                      string
	RedirectURIs                  []string
	ClientURI, LogoURI, PolicyURI string
	Created                       time.Time
}

// OAuthGrant is what a user let an app do.
type OAuthGrant struct {
	ID                         int64
	ClientID, ClientName, User string
	ClientURI                  string
	Scope                      string    // space-separated, as package oauth parses it
	Created, LastUsed          time.Time // LastUsed is the zero time before the first use
}

// OAuthCode is what a one-time code was made for.
type OAuthCode struct {
	GrantID                                       int64
	ClientID, User, Scope, RedirectURI, Challenge string
}

// OAuthAccess is who an access token is for.
type OAuthAccess struct {
	GrantID               int64
	ClientID, User, Scope string
}

// OAuthTokens are the secrets handed to an app. They are not kept; only their hashes are.
type OAuthTokens struct {
	Access, Refresh string
	ExpiresIn       int // seconds the access token lasts
	Scope           string
}

// OAuthEvictAfter is how old an app nobody connected must be before a full table may drop it for a new one.
const OAuthEvictAfter = 10 * time.Minute

// AddOAuthClient registers an app and gives it its id. The caller has already checked the addresses with package oauth.
func (in *Instance) AddOAuthClient(c OAuthClient) (*OAuthClient, error) {
	if c.Name == "" || len(c.RedirectURIs) == 0 || len(c.RedirectURIs) > oauth.MaxRedirects {
		return nil, ErrInvalid
	}
	for _, u := range c.RedirectURIs {
		if u == "" || strings.ContainsAny(u, "\n\r") {
			return nil, ErrInvalid
		}
	}
	c.ID, c.Created = oauth.NewClientID(), time.Now()
	// When the table is full, the oldest app that nobody has ever connected (and is not brand new) makes room: one
	// source registering thousands of apps then cannot keep real ones out. An app somebody connected is never evicted.
	if _, err := in.db.Exec(`DELETE FROM oauth_clients WHERE id=(SELECT id FROM oauth_clients
			WHERE created<? AND NOT EXISTS (SELECT 1 FROM oauth_grants WHERE client_id=oauth_clients.id) ORDER BY created LIMIT 1)
		AND (SELECT COUNT(*) FROM oauth_clients)>=?`, c.Created.Add(-OAuthEvictAfter).Unix(), MaxOAuthClients); err != nil {
		return nil, err
	}
	res, err := in.db.Exec(`INSERT INTO oauth_clients(id,name,redirect_uris,client_uri,logo_uri,policy_uri,created)
		SELECT ?,?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM oauth_clients)<?`,
		c.ID, c.Name, strings.Join(c.RedirectURIs, "\n"), c.ClientURI, c.LogoURI, c.PolicyURI, c.Created.Unix(), MaxOAuthClients)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrTooManyClients
	}
	return &c, nil
}

// OAuthClient returns a registered app.
func (in *Instance) OAuthClient(id string) (*OAuthClient, error) {
	var c OAuthClient
	var uris string
	var created int64
	err := in.rdb.QueryRow(`SELECT id,name,redirect_uris,client_uri,logo_uri,policy_uri,created FROM oauth_clients WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &uris, &c.ClientURI, &c.LogoURI, &c.PolicyURI, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.RedirectURIs, c.Created = strings.Split(uris, "\n"), time.Unix(created, 0)
	return &c, nil
}

// CreateOAuthGrant records that the user let the app do what scope says. It does nothing yet: it is the thing a code
// is made for, and it counts only once the code has been swapped for tokens.
func (in *Instance) CreateOAuthGrant(clientID, user, scope string) (int64, error) {
	res, err := in.db.Exec(`INSERT INTO oauth_grants(client_id,name,scope,created)
		SELECT ?,?,?,? WHERE (SELECT COUNT(*) FROM oauth_grants WHERE name=?)<?`, clientID, user, scope, time.Now().Unix(), user, MaxOAuthGrants)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrTooManyGrants
	}
	return res.LastInsertId()
}

// NewOAuthCode makes the one-time code for a grant, good for OAuthCodeTTL. challenge is the app's PKCE challenge.
func (in *Instance) NewOAuthCode(grantID int64, redirectURI, challenge string) (string, error) {
	code := oauth.NewSecret(oauth.PrefixCode)
	_, err := in.db.Exec(`INSERT INTO oauth_codes(hash,grant_id,redirect_uri,challenge,expires) VALUES(?,?,?,?,?)`,
		hashToken(code), grantID, redirectURI, challenge, time.Now().Add(OAuthCodeTTL).Unix())
	return code, err
}

// RedeemOAuthCode uses up a code. It works once: a second try with the same code means someone else has a copy, so
// the grant is revoked with everything issued from it. The caller must still check the app, the redirect address
// and the PKCE verifier against what it returns. A grant that was connected before by the same app for the same
// account is replaced by this one.
func (in *Instance) RedeemOAuthCode(code string) (*OAuthCode, error) {
	tx, err := in.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var c OAuthCode
	var expires, used int64
	err = tx.QueryRow(`SELECT c.grant_id,c.redirect_uri,c.challenge,c.expires,c.used,g.client_id,g.name,g.scope
		FROM oauth_codes c JOIN oauth_grants g ON g.id=c.grant_id WHERE c.hash=?`, hashToken(code)).
		Scan(&c.GrantID, &c.RedirectURI, &c.Challenge, &expires, &used, &c.ClientID, &c.User, &c.Scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCodeInvalid
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if used != 0 { // a code is never used twice: revoke what it made
		if _, err := tx.Exec(`DELETE FROM oauth_grants WHERE id=?`, c.GrantID); err != nil {
			return nil, err
		}
		return nil, commitThen(tx, ErrCodeInvalid)
	}
	if expires < now {
		return nil, ErrCodeInvalid
	}
	if _, err := tx.Exec(`UPDATE oauth_codes SET used=? WHERE hash=?`, now, hashToken(code)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM oauth_grants WHERE client_id=? AND name=? AND id<>?`, c.ClientID, c.User, c.GrantID); err != nil {
		return nil, err
	}
	return &c, tx.Commit()
}

// issue makes an access token and a refresh token for the grant, inside tx.
func issue(tx *sql.Tx, grantID int64, scope string) (*OAuthTokens, error) {
	now := time.Now()
	t := &OAuthTokens{Access: oauth.NewSecret(oauth.PrefixAccess), Refresh: oauth.NewSecret(oauth.PrefixRefresh),
		ExpiresIn: int(OAuthAccessTTL.Seconds()), Scope: scope}
	if _, err := tx.Exec(`INSERT INTO oauth_tokens(hash,grant_id,refresh,expires) VALUES(?,?,0,?),(?,?,1,?)`,
		hashToken(t.Access), grantID, now.Add(OAuthAccessTTL).Unix(), hashToken(t.Refresh), grantID, now.Add(OAuthRefreshTTL).Unix()); err != nil {
		return nil, err
	}
	return t, nil
}

// IssueOAuthTokens makes the first tokens of a grant, after its code was redeemed.
func (in *Instance) IssueOAuthTokens(grantID int64) (*OAuthTokens, error) {
	tx, err := in.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var scope string
	if err := tx.QueryRow(`SELECT scope FROM oauth_grants WHERE id=?`, grantID).Scan(&scope); err != nil {
		return nil, ErrTokenInvalid
	}
	t, err := issue(tx, grantID, scope)
	if err != nil {
		return nil, err
	}
	return t, tx.Commit()
}

// RefreshOAuthTokens swaps a refresh token for a new pair; the old refresh token and the old access tokens stop
// working. Using a refresh token that was already used (more than a moment ago) means two parties hold it, so the
// whole grant is revoked.
func (in *Instance) RefreshOAuthTokens(refresh, clientID string) (*OAuthTokens, error) {
	tx, err := in.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var grantID, expires, used int64
	var gclient, scope string
	err = tx.QueryRow(`SELECT t.grant_id,t.expires,t.used,g.client_id,g.scope FROM oauth_tokens t JOIN oauth_grants g ON g.id=t.grant_id
		WHERE t.hash=? AND t.refresh=1`, hashToken(refresh)).Scan(&grantID, &expires, &used, &gclient, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	switch {
	case gclient != clientID || expires < now.Unix():
		return nil, ErrTokenInvalid
	case used != 0 && now.Unix()-used <= int64(oauthRetry.Seconds()):
		return nil, ErrTokenInvalid // a retry of the same call
	case used != 0:
		if _, err := tx.Exec(`DELETE FROM oauth_grants WHERE id=?`, grantID); err != nil {
			return nil, err
		}
		return nil, commitThen(tx, ErrTokenInvalid)
	}
	if _, err := tx.Exec(`UPDATE oauth_tokens SET used=? WHERE hash=?`, now.Unix(), hashToken(refresh)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM oauth_tokens WHERE grant_id=? AND refresh=0`, grantID); err != nil {
		return nil, err
	}
	t, err := issue(tx, grantID, scope)
	if err != nil {
		return nil, err
	}
	return t, tx.Commit()
}

// OAuthAccess says who an access token is for, or ErrTokenInvalid if it is unknown or has expired. It is asked on
// every call of the API, so it only reads; the grant's last-used time is written at most every few minutes.
func (in *Instance) OAuthAccess(token string) (*OAuthAccess, error) {
	var a OAuthAccess
	var expires, lastUsed int64
	err := in.rdb.QueryRow(`SELECT t.grant_id,t.expires,g.client_id,g.name,g.scope,g.last_used FROM oauth_tokens t
		JOIN oauth_grants g ON g.id=t.grant_id WHERE t.hash=? AND t.refresh=0`, hashToken(token)).
		Scan(&a.GrantID, &expires, &a.ClientID, &a.User, &a.Scope, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if expires < now {
		return nil, ErrTokenInvalid
	}
	if now-lastUsed >= int64(oauthLastUsedEvery.Seconds()) {
		in.db.Exec(`UPDATE oauth_grants SET last_used=? WHERE id=?`, now, a.GrantID)
	}
	return &a, nil
}

// RevokeOAuthToken ends the grant that a token of the given app belongs to (RFC 7009). A token that is not known
// is not an error: the caller is told it worked either way.
func (in *Instance) RevokeOAuthToken(token, clientID string) error {
	_, err := in.db.Exec(`DELETE FROM oauth_grants WHERE client_id=? AND id IN (SELECT grant_id FROM oauth_tokens WHERE hash=?)`, clientID, hashToken(token))
	return err
}

// RevokeOAuthGrant ends one of the user's grants, with the tokens and codes made from it.
func (in *Instance) RevokeOAuthGrant(user string, id int64) error {
	res, err := in.db.Exec(`DELETE FROM oauth_grants WHERE id=? AND name=?`, id, user)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeOAuthGrants ends every grant of the user: when the account leaves the server, or its password changes.
func (in *Instance) RevokeOAuthGrants(user string) error {
	_, err := in.db.Exec(`DELETE FROM oauth_grants WHERE name=?`, user)
	return err
}

// OAuthGrants lists the apps the user has connected, newest first. A grant whose code was never swapped for tokens
// is not one yet.
func (in *Instance) OAuthGrants(user string) ([]OAuthGrant, error) {
	rows, err := in.rdb.Query(`SELECT g.id,g.client_id,c.name,c.client_uri,g.name,g.scope,g.created,g.last_used FROM oauth_grants g
		JOIN oauth_clients c ON c.id=g.client_id WHERE g.name=? AND EXISTS (SELECT 1 FROM oauth_codes WHERE grant_id=g.id AND used<>0)
		ORDER BY g.created DESC, g.id DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OAuthGrant
	for rows.Next() {
		var g OAuthGrant
		var created, last int64
		if err := rows.Scan(&g.ID, &g.ClientID, &g.ClientName, &g.ClientURI, &g.User, &g.Scope, &created, &last); err != nil {
			return nil, err
		}
		g.Created = time.Unix(created, 0)
		if last != 0 {
			g.LastUsed = time.Unix(last, 0)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// PruneOAuth forgets what can no longer be used: expired tokens, old codes, grants with nothing left, and apps that
// registered and were never connected.
func (in *Instance) PruneOAuth(now time.Time) error {
	n := now.Unix()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM oauth_tokens WHERE expires<?`, []any{n}},
		{`DELETE FROM oauth_codes WHERE expires<?`, []any{n - 3600}},
		// a grant is dead when it has no token and no code that can still be used; the minutes of grace cover one that
		// has just been made and has no code yet
		{`DELETE FROM oauth_grants WHERE created<? AND NOT EXISTS (SELECT 1 FROM oauth_tokens WHERE grant_id=oauth_grants.id)
			AND NOT EXISTS (SELECT 1 FROM oauth_codes WHERE grant_id=oauth_grants.id AND used=0 AND expires>=?)`, []any{n - 300, n}},
		{`DELETE FROM oauth_clients WHERE created<? AND NOT EXISTS (SELECT 1 FROM oauth_grants WHERE client_id=oauth_clients.id)`,
			[]any{n - int64(OAuthClientTTL.Seconds())}},
	} {
		if _, err := in.db.Exec(q.sql, q.args...); err != nil {
			return err
		}
	}
	return nil
}

// commitThen commits what the transaction did (a revocation) and then reports err, the reason for the refusal.
func commitThen(tx *sql.Tx, err error) error {
	if cerr := tx.Commit(); cerr != nil {
		return cerr
	}
	return err
}
