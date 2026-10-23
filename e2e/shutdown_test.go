package e2e

import (
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// The real server, told to stop with SIGTERM, finishes cleanly: it exits with status 0 and leaves a
// single file per database, without the -wal and -shm files that a killed server leaves behind.
func TestServeShutsDownCleanly(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "weft")
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/weft").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	data := filepath.Join(dir, "data")
	if out, err := exec.Command(bin, "user", "add", "-data", data, "alice").CombinedOutput(); err != nil {
		t.Fatalf("user add: %v\n%s", err, out)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	var log strings.Builder
	cmd := exec.Command(bin, "serve", "-addr", addr, "-data", data, "-domain", "localhost", "-poll", "0")
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer cmd.Process.Kill()

	up := false
	for i := 0; i < 100 && !up; i++ {
		req, _ := http.NewRequest("GET", "http://"+addr+"/feed.xml", nil)
		req.Host = "alice.localhost"
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
			up = res.StatusCode == 200
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !up {
		t.Fatalf("server did not come up:\n%s", log.String())
	}
	if _, err := os.Stat(filepath.Join(data, "instance.db-wal")); err != nil {
		t.Fatalf("while running there is a -wal file: %v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("exit after SIGTERM: %v\n%s", err, log.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("server did not stop:\n%s", log.String())
	}
	files := 0
	filepath.WalkDir(data, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(p) == "weft.lock" {
			return nil // the lock file stays; it holds nothing
		}
		files++
		if strings.HasSuffix(p, "-wal") || strings.HasSuffix(p, "-shm") {
			t.Errorf("%s left behind after a clean shutdown", p)
		}
		return nil
	})
	if files != 2 { // instance.db and alice.db
		t.Errorf("%d files in the data folder, want instance.db and alice.db", files)
	}
	if !strings.Contains(log.String(), "database closed") {
		t.Errorf("no sign the database was closed:\n%s", log.String())
	}
}

// A second server on the same data folder refuses to start, and the first goes on working.
func TestSecondServerOnTheSameFolderRefuses(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "weft")
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/weft").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	data := filepath.Join(dir, "data")
	if out, err := exec.Command(bin, "user", "add", "-data", data, "alice").CombinedOutput(); err != nil {
		t.Fatalf("user add: %v\n%s", err, out)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	first := exec.Command(bin, "serve", "-addr", addr, "-data", data, "-domain", "localhost", "-poll", "0")
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { first.Process.Signal(syscall.SIGTERM); first.Wait() }()
	up := false
	for i := 0; i < 100 && !up; i++ {
		req, _ := http.NewRequest("GET", "http://"+addr+"/feed.xml", nil)
		req.Host = "alice.localhost"
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
			up = res.StatusCode == 200
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !up {
		t.Fatal("the first server did not come up")
	}
	out, err := exec.Command(bin, "serve", "-addr", "127.0.0.1:0", "-data", data, "-poll", "0").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "in use by another weft server") {
		t.Fatalf("second server: err=%v\n%s", err, out)
	}
}

// The password of a user made or reset from the command line is temporary: the account is marked, so that the web
// UI makes the user choose their own at the first sign-in. With none given one is generated and shown on the
// console. -permanent opts out, and then needs a password to be given.
func TestUserAddAndPasswdMakeTemporaryPasswords(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "weft")
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/weft").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	data := filepath.Join(dir, "data")
	run := func(args ...string) (stdout, stderr string, err error) {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"user"}, args...)...)
		var o, e strings.Builder
		cmd.Stdout, cmd.Stderr = &o, &e
		err = cmd.Run()
		return o.String(), e.String(), err
	}
	must := func(args ...string) (string, string) {
		t.Helper()
		o, e, err := run(args...)
		if err != nil {
			t.Fatalf("weft user %v: %v\n%s%s", args, err, o, e)
		}
		return o, e
	}
	state := func(name, pw string) (marked, signsIn bool) {
		t.Helper()
		in, err := store.Open(data)
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		sp, release, err := in.Space(name)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		_, err = sp.CheckPassword(pw)
		return sp.MustChangePassword(), err == nil
	}

	// a given password is temporary, and not echoed
	out, errs := must("add", "-data", data, "-password", "temporary one", "bob")
	if !strings.Contains(errs, "bob must choose a new password at the first sign-in") || strings.Contains(out+errs, "temporary one") {
		t.Errorf("add with a password printed %q %q", out, errs)
	}
	if m, ok := state("bob", "temporary one"); !m || !ok {
		t.Errorf("bob: marked %v, signs in %v", m, ok)
	}
	// none given: one is made and shown, it works, and it is temporary
	out, errs = must("add", "-data", data, "dana")
	m := regexp.MustCompile(`temporary password for dana: ([a-z0-9]{5}-[a-z0-9]{5})\n`).FindStringSubmatch(errs)
	if m == nil || len(strings.TrimSpace(out)) != 48 { // stdout is still just the API token
		t.Fatalf("add without a password printed %q %q", out, errs)
	}
	if mk, ok := state("dana", m[1]); !mk || !ok {
		t.Errorf("dana with the shown password: marked %v, signs in %v", mk, ok)
	}
	// -permanent keeps the user's own password, and needs one
	must("add", "-data", data, "-password", "her own password", "-permanent", "carol")
	if mk, ok := state("carol", "her own password"); mk || !ok {
		t.Errorf("carol: marked %v, signs in %v", mk, ok)
	}
	if _, errs, err := run("add", "-data", data, "-permanent", "erin"); err == nil || !strings.Contains(errs, "-permanent needs -password") {
		t.Errorf("-permanent without a password: %v %q", err, errs)
	}
	if in, err := store.Open(data); err == nil {
		if in.UserExists("erin") {
			t.Errorf("a refused add left the user behind")
		}
		in.Close()
	}
	// passwd is temporary by default too, generates when asked for nothing, and -permanent opts out
	must("passwd", "-data", data, "-password", "reset by admin", "carol")
	if mk, ok := state("carol", "reset by admin"); !mk || !ok {
		t.Errorf("carol after passwd: marked %v, signs in %v", mk, ok)
	}
	_, errs = must("passwd", "-data", data, "bob")
	m = regexp.MustCompile(`temporary password for bob: (\S+)\n`).FindStringSubmatch(errs)
	if m == nil {
		t.Fatalf("passwd without a password printed %q", errs)
	}
	if mk, ok := state("bob", m[1]); !mk || !ok {
		t.Errorf("bob with the generated password: marked %v, signs in %v", mk, ok)
	}
	if _, ok := state("bob", "temporary one"); ok {
		t.Errorf("the old password still works")
	}
	must("passwd", "-data", data, "-password", "kept for good", "-permanent", "bob")
	if mk, ok := state("bob", "kept for good"); mk || !ok {
		t.Errorf("bob after passwd -permanent: marked %v, signs in %v", mk, ok)
	}
	if _, _, err := run("passwd", "-data", data, "-password", "short", "bob"); err == nil {
		t.Errorf("a short password was accepted")
	}
}
