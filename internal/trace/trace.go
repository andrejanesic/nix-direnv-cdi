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
)

// Mark appends a formatted line to "<self>.<pid>.ndctrace" next to the running
// binary. The pid suffix is essential: the same binary is invoked as several
// processes (gen, the hook, the re-exec'd mount child) under DIFFERENT uids
// under rootless podman, and a shared file created 0644 by one uid can't be
// appended to by another — so each process gets its own world-readable file.
func Mark(format string, args ...any) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	path := fmt.Sprintf("%s.%d.ndctrace", self, os.Getpid())
	// 0644 (subject to umask) so whatever uid creates it, the test user can read
	// it back; O_APPEND so multiple Marks in one process accrete.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}
