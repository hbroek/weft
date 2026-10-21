package e2e

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The real server, told to stop with SIGTERM, finishes cleanly: it exits with status 0 and leaves a
// single database file, without the -wal and -shm files that a killed server leaves behind.
func TestServeShutsDownCleanly(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "weft")
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/weft").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	db := filepath.Join(dir, "w.db")
	if out, err := exec.Command(bin, "user", "add", "-db", db, "alice").CombinedOutput(); err != nil {
		t.Fatalf("user add: %v\n%s", err, out)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	var log strings.Builder
	cmd := exec.Command(bin, "serve", "-addr", addr, "-db", db, "-domain", "localhost", "-poll", "0")
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
	if _, err := os.Stat(db + "-wal"); err != nil {
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
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(db + side); !os.IsNotExist(err) {
			t.Errorf("%s left behind after a clean shutdown: %v", side, err)
		}
	}
	if !strings.Contains(log.String(), "database closed") {
		t.Errorf("no sign the database was closed:\n%s", log.String())
	}
}
