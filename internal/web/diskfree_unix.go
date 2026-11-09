//go:build unix

package web

import "syscall"

// diskFree reports the free and the total bytes of the file system that holds path.
func diskFree(path string) (free, total int64, ok bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0, 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), true
}
