//go:build !unix

package store

// Lock does nothing where file locks are not available.
func Lock(dir string) (func(), error) { return func() {}, nil }
