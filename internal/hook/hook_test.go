package hook

// Unit tests for the hook core: the gate + mount-injection dispatch
// (run) and the entrypoint wrapper (wrapEntrypoint). No real container; the
// mount step is injected. The actual ns-entry mount is covered by integration
// tests.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	oci "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
)

func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n:\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":   "'plain'",
		"a b":     "'a b'",
		"a'b":     `'a'\''b'`,
		"":        "''",
		"$X`cmd`": "'$X`cmd`'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWrap_RelativeEntry_ShimContent(t *testing.T) {
	prefixDir := filepath.Join(t.TempDir(), "nixbin")
	writeExec(t, filepath.Join(prefixDir, "tool")) // dev-shell-only tool, host-accessible
	rootfs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfs, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	spec := &oci.Spec{Process: &oci.Process{
		Args: []string{"tool"},
		Env:  []string{"PATH=/usr/bin"},
	}}
	env := map[string]string{"CC": "gcc", "WEIRD": "a b'c"}

	if err := wrapEntrypoint(spec, rootfs, []string{prefixDir}, env); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}

	// Shim shadows the entry in the first image-PATH dir.
	shim := filepath.Join(rootfs, "usr/bin/tool")
	data, err := os.ReadFile(shim)
	if err != nil {
		t.Fatalf("shim not written: %v", err)
	}
	s := string(data)
	for _, want := range []string{
		"#!/bin/sh\n",
		"unset DIRENV_DIR DIRENV_DIFF",
		`export PATH="` + prefixDir + `:$PATH"`,
		"export CC='gcc'",
		`export WEIRD='a b'\''c'`, // single-quote escaped
		`exec "` + prefixDir + `/tool" "$@"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("shim missing %q\n---\n%s", want, s)
		}
	}
	if fi, _ := os.Stat(shim); fi.Mode().Perm()&0o111 == 0 {
		t.Error("shim must be executable")
	}
	// env exports are sorted (CC before WEIRD).
	if strings.Index(s, "export CC=") > strings.Index(s, "export WEIRD=") {
		t.Error("env exports should be sorted")
	}
}

func TestDebugLog_PrivatePermsAndNoSymlink(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "hook.log")
	dbg := debugLog(func(k string) (string, bool) {
		if k == "NDC_HOOK_LOG" {
			return logPath, true
		}
		return "", false
	})
	dbg("hello %d", 1)
	fi, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("log not written: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode = %v, want 0600", fi.Mode().Perm())
	}

	// A symlink at the log path must not be followed.
	sentinel := filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.log")
	if err := os.Symlink(sentinel, link); err != nil {
		t.Fatal(err)
	}
	debugLog(func(k string) (string, bool) {
		if k == "NDC_HOOK_LOG" {
			return link, true
		}
		return "", false
	})("through symlink")
	if got, _ := os.ReadFile(sentinel); string(got) != "keep\n" {
		t.Errorf("debug log wrote through symlink: %q", got)
	}
}

func TestValidEnvName(t *testing.T) {
	for _, ok := range []string{"CC", "_x", "FOO_BAR", "A1", "_"} {
		if !validEnvName(ok) {
			t.Errorf("validEnvName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "1ABC", "FOO BAR", "x=y", "a;b", "$(id)", "a`b`", "a-b", "a.b"} {
		if validEnvName(bad) {
			t.Errorf("validEnvName(%q) = true, want false", bad)
		}
	}
}

func TestWrap_SkipsUnsafeEnvName(t *testing.T) {
	prefixDir := filepath.Join(t.TempDir(), "nixbin")
	writeExec(t, filepath.Join(prefixDir, "tool"))
	rootfs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfs, "usr/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := &oci.Spec{Process: &oci.Process{
		Args: []string{"tool"},
		Env:  []string{"PATH=/usr/bin"},
	}}
	// A legitimate var plus an injection attempt smuggled in as a NAME.
	env := map[string]string{"CC": "gcc", "x=$(touch /tmp/pwned)": "v"}

	if err := wrapEntrypoint(spec, rootfs, []string{prefixDir}, env); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(rootfs, "usr/bin/tool"))
	if err != nil {
		t.Fatalf("shim not written: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "export CC='gcc'") {
		t.Errorf("valid var dropped:\n%s", s)
	}
	if strings.Contains(s, "touch /tmp/pwned") || strings.Contains(s, "$(") {
		t.Errorf("unsafe env name leaked into shim:\n%s", s)
	}
}

func TestWrap_AbsoluteInRootfs_T10(t *testing.T) {
	rootfs := t.TempDir()
	app := filepath.Join(rootfs, "usr/local/bin/app")
	writeExec(t, app)

	spec := &oci.Spec{Process: &oci.Process{
		Args: []string{"/usr/local/bin/app"},
		Env:  []string{"PATH=/usr/local/bin"},
	}}
	if err := wrapEntrypoint(spec, rootfs, []string{"/nix/store/p/bin"}, nil); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}

	// Real moved aside; in-place path is now a PATH-prepending shim.
	if _, err := os.Stat(app + ".real"); err != nil {
		t.Errorf("real binary not moved aside: %v", err)
	}
	data, _ := os.ReadFile(app)
	if !strings.Contains(string(data), `exec "/usr/local/bin/app.real" "$@"`) {
		t.Errorf("in-place shim wrong:\n%s", data)
	}
}

func TestWrap_AbsoluteIntoROStore_T9_NoOp(t *testing.T) {
	rootfs := t.TempDir() // empty: the /nix/store path is NOT present here
	spec := &oci.Spec{Process: &oci.Process{
		Args: []string{"/nix/store/abc/bin/tool"},
		Env:  []string{"PATH=/usr/bin"},
	}}
	if err := wrapEntrypoint(spec, rootfs, []string{"/nix/store/abc/bin"}, nil); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootfs, "nix/store/abc/bin/tool")); err == nil {
		t.Error("T9: must not create/wrap an absolute RO-store entrypoint")
	}
}

func TestWrap_NothingToDo(t *testing.T) {
	rootfs := t.TempDir()
	// No prefix and no env -> no-op.
	spec := &oci.Spec{Process: &oci.Process{Args: []string{"sh"}, Env: []string{"PATH=/bin"}}}
	if err := wrapEntrypoint(spec, rootfs, nil, nil); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}
	if entries, _ := os.ReadDir(rootfs); len(entries) != 0 {
		t.Errorf("expected no writes, got %v", entries)
	}
}

// TestRun_GateClosed: with no DIRENV_DIR the device is inert — no mount, no wrap.
func TestRun_GateClosed(t *testing.T) {
	called := false
	mount := func(pid int, rootfs string, closure []string) error { called = true; return nil }
	getenv := func(k string) (string, bool) { return "", false } // nothing set

	rootfs := t.TempDir()
	spec := &oci.Spec{Process: &oci.Process{Args: []string{"sh"}, Env: []string{"PATH=/bin"}}}
	state := &oci.State{Pid: 123, Bundle: t.TempDir()}

	if err := run(state, spec, rootfs, getenv, mount); err != nil {
		t.Fatalf("run: %v", err)
	}
	if called {
		t.Error("gate closed: mount must NOT be called without DIRENV_DIR")
	}
	if entries, _ := os.ReadDir(rootfs); len(entries) != 0 {
		t.Error("gate closed: nothing should be written")
	}
}

// TestRun_GateOpen_MountsClosure: with DIRENV_DIR + a mounts.json, run injects
// the closure (mount called with the right pid/rootfs/closure).
func TestRun_GateOpen_MountsClosure(t *testing.T) {
	project := t.TempDir()
	closure := []string{"/nix/store/aaa", "/nix/store/bbb"}
	if err := devshell.WriteMounts(filepath.Join(project, ".direnv", "cdi", "mounts.json"), closure); err != nil {
		t.Fatal(err)
	}

	var gotPid int
	var gotRootfs string
	var gotClosure []string
	mount := func(pid int, rootfs string, c []string) error {
		gotPid, gotRootfs, gotClosure = pid, rootfs, c
		return nil
	}
	getenv := func(k string) (string, bool) {
		if k == "DIRENV_DIR" {
			return "-" + project, true // direnv's leading '-' marker
		}
		return "", false // no DIRENV_DIFF -> wrap is skipped, mount still runs
	}

	rootfs := t.TempDir()
	spec := &oci.Spec{Process: &oci.Process{Args: []string{"sh"}, Env: []string{"PATH=/bin"}}}
	state := &oci.State{Pid: 4242, Bundle: t.TempDir()}

	if err := run(state, spec, rootfs, getenv, mount); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotPid != 4242 {
		t.Errorf("mount pid = %d, want 4242", gotPid)
	}
	if gotRootfs != rootfs {
		t.Errorf("mount rootfs = %q, want %q", gotRootfs, rootfs)
	}
	if strings.Join(gotClosure, ",") != strings.Join(closure, ",") {
		t.Errorf("mount closure = %v, want %v", gotClosure, closure)
	}
}

// TestRun_NilSpec_MountsButNoWrap: when config.json is unreachable (rootless
// podman bundle="/" and the derived path failed), Run passes spec=nil. The hook
// must NOT crash and must still inject the closure mount (which needs only
// pid+rootfs+closure); the entrypoint wrap is the only part that is skipped.
func TestRun_NilSpec_MountsButNoWrap(t *testing.T) {
	project := t.TempDir()
	closure := []string{"/nix/store/aaa"}
	if err := devshell.WriteMounts(filepath.Join(project, ".direnv", "cdi", "mounts.json"), closure); err != nil {
		t.Fatal(err)
	}

	mounted := false
	mount := func(pid int, rootfs string, c []string) error { mounted = true; return nil }
	getenv := func(k string) (string, bool) {
		if k == "DIRENV_DIR" {
			return "-" + project, true
		}
		return "", false
	}

	rootfs := t.TempDir()
	state := &oci.State{Pid: 7, Bundle: "/"}

	if err := run(state, nil, rootfs, getenv, mount); err != nil {
		t.Fatalf("run with nil spec: %v", err)
	}
	if !mounted {
		t.Error("nil spec must still inject the closure mount")
	}
	if entries, _ := os.ReadDir(rootfs); len(entries) != 0 {
		t.Error("nil spec: no entrypoint wrap should be written")
	}
}

func TestRun_GateOpen_FromProcessEnv(t *testing.T) {
	project := t.TempDir()
	closure := []string{"/nix/store/aaa"}
	if err := devshell.WriteMounts(filepath.Join(project, ".direnv", "cdi", "mounts.json"), closure); err != nil {
		t.Fatal(err)
	}

	called := false
	mount := func(pid int, rootfs string, c []string) error {
		called = true
		if strings.Join(c, ",") != strings.Join(closure, ",") {
			t.Errorf("mount closure = %v, want %v", c, closure)
		}
		return nil
	}
	getenv := func(k string) (string, bool) { return "", false }

	rootfs := t.TempDir()
	spec := &oci.Spec{Process: &oci.Process{
		Args: []string{"sh"},
		Env:  []string{"PATH=/bin", "DIRENV_DIR=-" + project},
	}}
	state := &oci.State{Pid: 4242, Bundle: t.TempDir()}

	if err := run(state, spec, rootfs, getenv, mount); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !called {
		t.Error("process DIRENV_DIR fallback should open the gate")
	}
}
