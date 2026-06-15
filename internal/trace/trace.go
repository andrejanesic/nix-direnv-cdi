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

// Mark appends a formatted line to "<self>.ndctrace" next to the running binary.
func Mark(format string, args ...any) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	// 0666 (subject to umask) so a sub-uid hook and the test user can both
	// touch/read it; O_APPEND so concurrent writers (hook + re-exec'd child)
	// don't clobber each other.
	f, err := os.OpenFile(self+".ndctrace", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, format+"\n", args...)
}
