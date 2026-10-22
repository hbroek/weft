//go:build unix

package store

import "syscall"

// openFileLimit raises the soft limit on open files as far as it may go (to 65536 at most) and returns
// what the process has after that, or 0 if it cannot tell.
func openFileLimit() int {
	var l syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &l); err != nil {
		return 0
	}
	want := uint64(65536)
	if l.Max < want {
		want = l.Max
	}
	if l.Cur < want {
		n := l
		n.Cur = want
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &n); err == nil {
			l = n
		}
	}
	return int(l.Cur)
}
