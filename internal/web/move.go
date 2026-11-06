package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hbroek/weft/internal/img"
	"github.com/hbroek/weft/internal/store"
)

// Moving an account to another server: the part the web server plays. docs/plan.md, iteration 10, has the
// whole flow. The old server (the one the account leaves) answers the calls under /.well-known/weft/move/;
// the new server (where the account is going) makes them, from moveReceive.

const movePath = "/.well-known/weft/move/"

// moveManifest describes the copy on offer.
type moveManifest struct {
	Format      int    `json:"format"`
	Schema      int    `json:"schema"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Fingerprint string `json:"fingerprint"`
}

func manifestOf(name string, m *store.Move) moveManifest {
	return moveManifest{Format: store.ExportFormat, Schema: m.Schema, Name: name, Size: m.Size, SHA256: m.SHA256, Fingerprint: m.Fingerprint}
}

// refusesChanges is true while the account may not be changed: the last round trips of a move, and after them
// until the user cancels or the move is done.
func (s *Server) refusesChanges(name string, r *http.Request) bool {
	switch s.st.Dir(name).Move {
	case store.MoveLock, store.MoveReceived:
	default:
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	p := r.URL.Path
	return !(strings.HasPrefix(p, movePath) || p == "/webmention" || p == "/move/cancel" || p == "/move/finish" || p == "/login" || p == "/logout")
}

// ---- the old server ----

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// serverAddress turns what a person typed for a server ("weftbig.com", "https://weftbig.com/",
// "localhost:8110") into the origin the account will have there: this server's scheme, the user's name, then the
// server.
func (s *Server) serverAddress(name, typed string) (string, bool) {
	t := strings.ToLower(strings.TrimSpace(typed))
	t = strings.TrimPrefix(strings.TrimPrefix(t, "https://"), "http://")
	t = strings.Trim(t, "/")
	if t == "" || strings.ContainsAny(t, "/?# @\\") {
		return "", false
	}
	t = strings.TrimPrefix(t, strings.ToLower(name)+".") // the address of the account, as seen in the browser, is meant too
	if _, err := store.Origin(s.cfg.Scheme + "://" + t); err != nil {
		return "", false
	}
	return s.cfg.Scheme + "://" + name + "." + t, true
}

// moveStep answers one call of the new server.
func (s *Server) moveStep(w http.ResponseWriter, r *http.Request, q *req) {
	name := q.host.Name
	step := r.PathValue("step")
	if step == "prepare" {
		s.movePrepare(w, r, q)
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	m, err := s.st.MoveAuth(name, tok)
	if !ok || err != nil {
		http.Error(w, "move token not accepted", http.StatusForbidden)
		return
	}
	switch step {
	case "lock":
		s.moveLock(w, q, m)
	case "received":
		if m.State != store.MoveLock {
			http.Error(w, "not locked", http.StatusConflict)
			return
		}
		if err := s.st.MoveSetState(name, store.MoveReceived, 0); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonOut(w, 200, map[string]bool{"ok": true})
	case "done":
		if m.State != store.MoveReceived {
			http.Error(w, "not received", http.StatusConflict)
			return
		}
		from, to := s.base(r), m.Dest
		if err := s.st.MoveFinish(name); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		go s.forwardHeld(name, from, to)
		jsonOut(w, 200, map[string]bool{"ok": true})
	case "abort":
		if m.State != store.MoveHold && m.State != store.MoveLock {
			http.Error(w, "too late to stop", http.StatusConflict)
			return
		}
		s.st.MoveAbort(name)
		jsonOut(w, 200, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) movePrepare(w http.ResponseWriter, r *http.Request, q *req) {
	name := q.host.Name
	var in struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	tok, err := s.st.MovePrepare(name, r.Header.Get("X-Weft-Move-Code"), in.To)
	if err != nil {
		log.Printf("move of %s refused: %v", name, err)
		http.Error(w, "move code not accepted", http.StatusForbidden)
		return
	}
	cp, err := s.st.MakeCopy(q.sp, s.cfg.BaseDomain)
	if err == nil {
		err = s.st.MoveStore(name, cp)
	}
	if err != nil {
		s.st.MoveAbort(name)
		http.Error(w, "could not make a copy: "+err.Error(), 500)
		return
	}
	m, err := s.st.MovePending(name)
	if err != nil {
		s.st.MoveAbort(name)
		http.Error(w, err.Error(), 500)
		return
	}
	jsonOut(w, 200, map[string]any{"token": tok, "manifest": manifestOf(name, m)})
}

// moveData sends the copy; the Range header resumes a broken download.
func (s *Server) moveData(w http.ResponseWriter, r *http.Request, q *req) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	m, err := s.st.MoveAuth(q.host.Name, tok)
	if err != nil || (m.State != store.MoveHold && m.State != store.MoveLock) || m.File == "" {
		http.Error(w, "move token not accepted", http.StatusForbidden)
		return
	}
	f, err := os.Open(s.st.CopyPath(m.File))
	if err != nil {
		http.Error(w, "no copy", http.StatusNotFound)
		return
	}
	defer f.Close()
	if m.State == store.MoveHold {
		s.st.MoveTouch(q.host.Name)
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// moveLock is the start of the short window: changes stop, and if anything changed since the copy was made a
// new copy is made, while the lock holds.
func (s *Server) moveLock(w http.ResponseWriter, q *req, m *store.Move) {
	name := q.host.Name
	if m.State != store.MoveHold && m.State != store.MoveLock {
		http.Error(w, "not under way", http.StatusConflict)
		return
	}
	if err := s.st.MoveSetState(name, store.MoveLock, store.MoveLockLife()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	time.Sleep(250 * time.Millisecond) // a change that was being made when the lock came finishes
	fp, err := q.sp.Fingerprint()
	if err != nil {
		s.st.MoveAbort(name)
		http.Error(w, err.Error(), 500)
		return
	}
	changed := fp != m.Fingerprint
	if changed {
		cp, err := s.st.MakeCopy(q.sp, s.cfg.BaseDomain)
		if err == nil {
			err = s.st.MoveStore(name, cp)
		}
		if err != nil {
			s.st.MoveAbort(name)
			http.Error(w, err.Error(), 500)
			return
		}
		if m, err = s.st.MovePending(name); err != nil {
			s.st.MoveAbort(name)
			http.Error(w, err.Error(), 500)
			return
		}
	}
	jsonOut(w, 200, map[string]any{"changed": changed, "manifest": manifestOf(name, m)})
}

// forwardHeld sends the Webmentions that arrived while the account moved on to the new server, as new
// Webmentions: the same source, and the target at the new address.
func (s *Server) forwardHeld(name, from, to string) {
	held, err := s.st.TakeHeld(name)
	if err != nil || len(held) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, h := range held {
		target := h.Target
		if rest, ok := strings.CutPrefix(target, from); ok {
			target = to + rest
		}
		for try := 0; try < 3; try++ {
			code, err := s.web.PostForm(ctx, to+"/webmention", url.Values{"source": {h.Source}, "target": {target}})
			if err == nil && code/100 == 2 {
				break
			}
			time.Sleep(time.Duration(try+1) * time.Second)
		}
	}
}

// ---- the user's pages ----

// moveView is the "Moving" part of the profile page.
type moveView struct {
	State, Dest, This string
	Waiting           bool   // a code was made and not used yet
	Code              string // the code, on the page that makes it
	Error, Notice     string
	Receive           bool   // this account is empty, so a move can come here
	Why               string // what is in the way, when it is not
	Pulling           bool
}

func (s *Server) moveView(r *http.Request, q *req) *moveView {
	v := &moveView{This: strings.TrimPrefix(r.Host, q.host.Name+".")}
	if m, err := s.st.MovePending(q.host.Name); err == nil {
		v.State, v.Dest, v.Waiting = m.State, m.Dest, m.State == ""
	}
	what := q.sp.WhatIsHere()
	v.Receive, v.Why = len(what) == 0, strings.Join(what, ", ")
	v.Pulling = s.pulls.get(q.host.Name) != nil
	return v
}

func (s *Server) moveMsg(w http.ResponseWriter, r *http.Request, q *req, status int, errMsg, notice, code string) {
	d := s.data(r, q, "Profile")
	d.Profile.Display, d.Profile.Bio, d.Profile.Avatar = q.host.Display, q.host.Bio, q.host.Avatar
	if localAvatar(q.host.Avatar) != "" {
		d.Profile.Avatar, d.Profile.Local = "", true
	}
	d.UploadMax = s.limitFor(img.Compact)
	d.Domain = s.domainView(r, q)
	d.Move = s.moveView(r, q)
	d.Move.Error, d.Move.Notice, d.Move.Code = errMsg, notice, code
	s.show(w, "profile", status, d)
}

// moveCode gives the owner a move code, after the password.
func (s *Server) moveCode(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if blocked, wait := s.guessBlocked(w, r, q); blocked {
		s.moveMsg(w, r, q, http.StatusTooManyRequests, "Too many wrong passwords. Try again in "+waitText(wait)+".", "", "")
		return
	}
	_, err := q.sp.CheckPassword(r.FormValue("password"))
	s.guessed(r, q, err == nil)
	if err != nil {
		s.moveMsg(w, r, q, http.StatusBadRequest, "The password is not right.", "", "")
		return
	}
	dest, ok := s.serverAddress(q.host.Name, r.FormValue("server"))
	if !ok {
		s.moveMsg(w, r, q, http.StatusBadRequest, "Give the new server as a name such as weftbig.com.", "", "")
		return
	}
	code, err := s.st.MoveCode(q.host.Name, dest)
	switch {
	case errors.Is(err, store.ErrMoveBusy):
		s.moveMsg(w, r, q, http.StatusConflict, "A move is already under way. Cancel it first.", "", "")
	case err != nil:
		s.moveMsg(w, r, q, http.StatusInternalServerError, "Could not make a code: "+err.Error(), "", "")
	default:
		s.moveMsg(w, r, q, http.StatusOK, "", "", code)
	}
}

// moveCancel ends a move at the user's wish, at any step before it is done.
func (s *Server) moveCancel(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if err := s.st.MoveAbort(q.host.Name); err != nil {
		s.moveMsg(w, r, q, http.StatusInternalServerError, err.Error(), "", "")
		return
	}
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
}

// moveFinish completes a move whose copy was received but whose last call never came: the account is
// already at the new address. It is for the owner to press when the new server says so.
func (s *Server) moveFinish(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	m, err := s.st.MovePending(q.host.Name)
	if err != nil || m.State != store.MoveReceived {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return
	}
	from, to := s.base(r), m.Dest
	if err := s.st.MoveFinish(q.host.Name); err != nil {
		s.moveMsg(w, r, q, http.StatusInternalServerError, err.Error(), "", "")
		return
	}
	go s.forwardHeld(q.host.Name, from, to)
	http.Redirect(w, r, to+"/", http.StatusSeeOther)
}

// ---- the new server ----

// pullStatus is how far a move to this server has got.
type pullStatus struct {
	mu   sync.Mutex
	step string
	err  string
	done bool
	// stuck is set when the old server holds the account and only its owner can free it.
	stuck bool
}

func (p *pullStatus) set(step string) { p.mu.Lock(); p.step = step; p.mu.Unlock() }
func (p *pullStatus) fail(err error, stuck bool) {
	p.mu.Lock()
	p.err, p.stuck = err.Error(), stuck
	p.mu.Unlock()
}
func (p *pullStatus) finish() { p.mu.Lock(); p.done, p.step = true, "Done"; p.mu.Unlock() }
func (p *pullStatus) view() pullView {
	p.mu.Lock()
	defer p.mu.Unlock()
	return pullView{Step: p.step, Err: p.err, Done: p.done, Stuck: p.stuck}
}

type pullView struct {
	Step, Err   string
	Done, Stuck bool
}

type pullSet struct {
	mu sync.Mutex
	m  map[string]*pullStatus
}

func (ps *pullSet) get(name string) *pullStatus {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.m[name]
}

// start registers a new move for the account, unless one is running.
func (ps *pullSet) start(name string) *pullStatus {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p := ps.m[name]; p != nil {
		p.mu.Lock()
		running := !p.done && p.err == ""
		p.mu.Unlock()
		if running {
			return nil
		}
	}
	if ps.m == nil {
		ps.m = map[string]*pullStatus{}
	}
	p := &pullStatus{step: "Starting"}
	ps.m[name] = p
	return p
}

// moveReceive starts the move of an account here, after the old server and the code are given.
func (s *Server) moveReceive(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	name := q.host.Name
	old, ok := s.serverAddress(name, r.FormValue("server"))
	code := strings.TrimSpace(r.FormValue("code"))
	switch {
	case !ok || code == "":
		s.moveMsg(w, r, q, http.StatusBadRequest, "Give the old server (such as weftseed.com) and the move code.", "", "")
		return
	case len(q.sp.WhatIsHere()) > 0:
		s.moveMsg(w, r, q, http.StatusConflict, "This account is not empty ("+strings.Join(q.sp.WhatIsHere(), ", ")+"). Remove those first: the move replaces everything here.", "", "")
		return
	}
	p := s.pulls.start(name)
	if p == nil {
		http.Redirect(w, r, "/move/status", http.StatusSeeOther)
		return
	}
	go func() {
		if err := s.pull(name, old, code, s.base(r), p); err != nil {
			var st stuckError
			p.fail(err, errors.As(err, &st))
		}
	}()
	http.Redirect(w, r, "/move/status", http.StatusSeeOther)
}

func (s *Server) moveStatus(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	p := s.pulls.get(q.host.Name)
	if p == nil {
		http.Redirect(w, r, "/profile", http.StatusSeeOther)
		return
	}
	d := s.data(r, q, "Moving here")
	v := p.view()
	d.Pull = &v
	s.show(w, "movestatus", http.StatusOK, d)
}

// stuckError marks a failure after which the old server holds the account, so only its owner can free it.
type stuckError struct{ error }

func (e stuckError) Unwrap() error { return e.error }

// moveCall makes one call of the move protocol on the old server. Redirects are not followed: a 301 on the last
// step means the account already left.
func (s *Server) moveCall(ctx context.Context, method, old, step, token string, hdr map[string]string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, old+movePath+step, rd)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "Weft/0.1 (+https://weftseed.com)")
	return s.web.Stream().Do(req)
}

// moveJSON makes a call and decodes the JSON answer, or turns a refusal into an error.
func (s *Server) moveJSON(ctx context.Context, old, step, token string, hdr map[string]string, body, out any) error {
	res, err := s.moveCall(ctx, "POST", old, step, token, hdr, body)
	if err != nil {
		return fmt.Errorf("%s: %w", step, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		hint := ""
		if res.StatusCode == http.StatusNotFound {
			hint = " (check the old server's address: give the server, such as weftseed.com, and the move code made there)"
		}
		return fmt.Errorf("the old server refused (%s): %s%s", res.Status, strings.TrimSpace(string(data)), hint)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// download fetches the copy, checks its size and checksum, and unpacks and checks it.
func (s *Server) download(ctx context.Context, old, token, name string, m moveManifest) (*store.Staged, error) {
	if m.Format != store.ExportFormat || m.Name != name {
		return nil, fmt.Errorf("the old server offers something this server cannot take (format %d, account %q)", m.Format, m.Name)
	}
	if m.Schema > store.SpaceSchemaVersion {
		return nil, fmt.Errorf("the account is from a newer Weft (schema %d, this one knows %d): upgrade this server first", m.Schema, store.SpaceSchemaVersion)
	}
	if m.Size <= 0 || m.Size > store.MaxImportBytes {
		return nil, fmt.Errorf("the account is %d bytes, more than this server takes", m.Size)
	}
	dir := s.st.CopyPath("moves")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".incoming.zip")
	defer os.Remove(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	var have int64
	for attempt := 0; have < m.Size && attempt < 5; attempt++ { // a broken download resumes where it stopped
		hdr := map[string]string{}
		if have > 0 {
			hdr["Range"] = fmt.Sprintf("bytes=%d-", have)
		}
		res, err := s.moveCall(ctx, "GET", old, "data", token, hdr, nil)
		if err != nil {
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		if (have == 0 && res.StatusCode != http.StatusOK) || (have > 0 && res.StatusCode != http.StatusPartialContent) {
			res.Body.Close()
			return nil, fmt.Errorf("the old server would not send the data (%s)", res.Status)
		}
		n, _ := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, m.Size-have))
		res.Body.Close()
		have += n
	}
	if have != m.Size {
		return nil, fmt.Errorf("the download stopped at %d of %d bytes", have, m.Size)
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return nil, errors.New("the download does not match its checksum")
	}
	st, err := s.st.StageExport(path)
	if err != nil {
		return nil, err
	}
	if st.Manifest.Name != name {
		st.Close()
		return nil, fmt.Errorf("the file holds the account %q, not %q", st.Manifest.Name, name)
	}
	if q := s.mediaQuota(); q > 0 && st.MediaBytes > q {
		st.Close()
		return nil, fmt.Errorf("the pictures take %s, and this server allows %s each", byteSize(st.MediaBytes), byteSize(q))
	}
	return st, nil
}

// pull moves the account here: the steps of the plan, from the new server's side.
func (s *Server) pull(name, old, code, newOrigin string, p *pullStatus) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()

	p.set("Asking the old server")
	var pre struct {
		Token    string       `json:"token"`
		Manifest moveManifest `json:"manifest"`
	}
	if err := s.moveJSON(ctx, old, "prepare", "", map[string]string{"X-Weft-Move-Code": code}, map[string]string{"to": newOrigin}, &pre); err != nil {
		return err
	}
	token := pre.Token
	locked := false
	defer func() { // until the old server has a good copy, a failure lets it go
		if err != nil && !locked {
			s.moveJSON(context.Background(), old, "abort", token, nil, nil, nil)
		}
	}()

	p.set("Downloading your account")
	st, err := s.download(ctx, old, token, name, pre.Manifest)
	if err != nil {
		return err
	}
	defer func() { st.Close() }()

	p.set("Stopping changes on the old server for a moment")
	for pass := 0; ; pass++ {
		var lk struct {
			Changed  bool         `json:"changed"`
			Manifest moveManifest `json:"manifest"`
		}
		if err := s.moveJSON(ctx, old, "lock", token, nil, nil, &lk); err != nil {
			return err
		}
		if !lk.Changed {
			break
		}
		if pass >= 2 {
			return errors.New("your account kept changing while it was copied; try again when nothing is being posted")
		}
		p.set("Something changed meanwhile; downloading again")
		st.Close()
		if st, err = s.download(ctx, old, token, name, lk.Manifest); err != nil {
			st = &store.Staged{}
			return err
		}
	}

	p.set("Checking the copy")
	if err := s.moveJSON(ctx, old, "received", token, nil, nil, nil); err != nil {
		return err
	}
	locked = true // from here only the user can free the old account
	p.set("Putting your account in place")
	if err := s.st.ReplaceSpace(name, st.Path); err != nil {
		return stuckError{fmt.Errorf("the copy could not be put in place: %w. Cancel the move on the old server (Profile) and try again", err)}
	}

	p.set("Telling the old server it is done")
	var last error
	for try := 0; try < 5; try++ {
		res, err := s.moveCall(ctx, "POST", old, "done", token, nil, nil)
		if err == nil {
			code := res.StatusCode
			res.Body.Close()
			if code == http.StatusOK || code == http.StatusMovedPermanently || code == http.StatusPermanentRedirect {
				p.finish()
				return nil
			}
			last = fmt.Errorf("the old server answered %d", code)
		} else {
			last = err
		}
		time.Sleep(time.Duration(try+1) * 2 * time.Second)
	}
	return stuckError{fmt.Errorf("your account is here, but the old server was not told (%v). Open Profile on the old server and press 'Finish the move'", last)}
}
