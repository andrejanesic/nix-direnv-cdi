//go:build unix

package hook

import "golang.org/x/sys/unix"

// unixAccessWritable reports whether the calling process can write to path,
// using the real access(2) check against the effective uid/gid. This is the
// faithful equivalent of the MVP's `[ -w "$dir" ]` guard. The hook only ever
// runs on Linux (OCI createRuntime), but the build tag keeps the package
// buildable on other unixes too.
func unixAccessWritable(path string) bool {
	return unix.Access(path, unix.W_OK) == nil
}
