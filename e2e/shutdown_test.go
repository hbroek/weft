package e2e

import (
	"io/fs"
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
