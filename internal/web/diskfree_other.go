//go:build !unix

package web

func diskFree(path string) (free, total int64, ok bool) { return 0, 0, false }
