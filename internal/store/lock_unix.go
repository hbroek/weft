//go:build unix

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes the data folder for one server: a second `weft serve` (or a restore, or anything else that
// must be alone) on the same folder fails here instead of fighting over the files. The lock is held
// until the returned function is called or the process ends, even by a crash. It is a lock on a file
// in the folder, so it does not cover a folder shared over a network file system.
func Lock(dir string) (unlock func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "weft.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("data folder %s is in use by another weft server", dir)
	}
	return func() { f.Close() }, nil // closing the file releases the lock
}
