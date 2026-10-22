package store

import (
	"runtime"
	"testing"
)

func TestLockAllowsOneHolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no file locks")
	}
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir); err == nil {
		t.Fatal("a second holder got the lock")
	}
	unlock()
	again, err := Lock(dir)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}
