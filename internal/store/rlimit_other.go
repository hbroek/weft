//go:build !unix

package store

func openFileLimit() int { return 0 }
