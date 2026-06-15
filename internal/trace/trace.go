// Package trace is a best-effort, environment-independent breadcrumb for
// diagnosing the createRuntime hook, whose stderr crun discards and whose
// NDC_HOOK_LOG channel may not survive the runtime's env handling. It appends to
// "<self-binary-path>.ndctrace" (located via /proc/self/exe), a path a test
// already knows from the binary it built, so the hook's progress is recoverable
// regardless of how env propagates. No-op on any error; remove once the hook is
// understood on the target runtime.
package trace

import (
	"fmt"
	"os"
	"strings"
)

// ArgPrefix is the argv flag carrying the trace base path. Passing the path via
// argv (vs deriving it from os.Executable) makes Mark independent of /proc,
// which a createRuntime hook may not have mounted — the case that defeated the
// os.Executable channel.
const ArgPrefix = "--ndctrace="

// base returns the trace base path: the argv value if present (no /proc needed),
// else "<self-binary>" via os.Executable. Returns "" only if neither is
// available.
func base() string {
	for _, a := range os.Args {
		if v, ok := strings.CutPrefix(a, ArgPrefix); ok {
			return v
		}
	}
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	return self
}

// Mark appends a formatted line to "<base>.<pid>.ndctrace". The pid suffix is
// essential: the same binary runs as several processes (gen, the hook, the
// re-exec'd mount child) under DIFFERENT uids under rootless podman, and a
// shared file created 0644 by one uid can't be appended to by another — so each
// process gets its own world-readable file.
func Mark(format string, args ...any) {
	b := base()
	if b == "" {
		return
	}
	path := fmt.Sprintf("%s.%d.ndctrace", b, os.Getpid())
	// 0644 (subject to umask) so whatever uid creates it, the test user can read
	// it back; O_APPEND so multiple Marks in one process accrete.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}
