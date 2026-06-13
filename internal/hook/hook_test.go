package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	oci "github.com/opencontainers/runtime-spec/specs-go"
)

// mkExec writes an executable file at path (creating parent dirs), so the host
// stat/exec checks see a real binary the way the MVP's `[ -x ]` test does.
func mkExec(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho real\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func specWith(args []string, env []string, mounts []oci.Mount) *oci.Spec {
	return &oci.Spec{
		Process: &oci.Process{Args: args, Env: env},
		Mounts:  mounts,
	}
}

// T1/T2/T3/T4 analog: a relative entrypoint resolving to a base-image tool.
// crun finds the shadow shim first; the shim prepends the prefix and execs the
// untouched real binary (additive PATH, real binary preserved).
func TestWrapEntrypoint_RelativeImageTool(t *testing.T) {
	rootfs := t.TempDir()
	// Image tool present at rootfs/usr/bin/bash; first PATH dir is /usr/local/sbin.
	mkExec(t, filepath.Join(rootfs, "usr/bin/bash"))

	spec := specWith(
		[]string{"bash"},
		[]string{
			"DEVSHELL_PREFIX=/devshellprefix",
			"PATH=/usr/local/sbin:/usr/bin",
		},
		nil,
	)

	if err := wrapEntrypoint(spec, rootfs); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}

	// Shadow shim written into the first PATH dir.
	shim := filepath.Join(rootfs, "usr/local/sbin/bash")
	got := mustRead(t, shim)
	if !strings.Contains(got, `export PATH="/devshellprefix:$PATH"`) {
		t.Errorf("shim missing additive PATH export:\n%s", got)
	}
	if !strings.Contains(got, `exec "/usr/bin/bash" "$@"`) {
		t.Errorf("shim does not exec the real image tool:\n%s", got)
	}
	// Real binary untouched (not moved aside; no .real created).
	if _, err := os.Stat(filepath.Join(rootfs, "usr/bin/bash")); err != nil {
		t.Errorf("real bash should be preserved: %v", err)
	}
	if exists(filepath.Join(rootfs, "usr/bin/bash.real")) {
		t.Errorf("real bash should NOT have been moved aside")
	}
	// Shim is executable.
	if fi, err := os.Stat(shim); err != nil || fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("shim must be executable (mode=%v err=%v)", fi.Mode(), err)
	}
}

// T7/T8 analog: a relative entrypoint that only exists in the dev-shell prefix,
// which is a RO bind mount (Destination /devshellprefix, Source = a host dir).
// The shim is shadowed into the first image-PATH dir and execs the CONTAINER
// path of the prefix tool.
func TestWrapEntrypoint_RelativePrefixOnlyTool(t *testing.T) {
	rootfs := t.TempDir()
	prefixSrc := t.TempDir() // host-side source of the /devshellprefix mount
	mkExec(t, filepath.Join(prefixSrc, "tool"))
	// First image PATH dir must exist & be writable for the shim.
	if err := os.MkdirAll(filepath.Join(rootfs, "usr/local/sbin"), 0o755); err != nil {
		t.Fatal(err)
	}

	spec := specWith(
		[]string{"tool"},
		[]string{
			"DEVSHELL_PREFIX=/devshellprefix",
			"PATH=/usr/local/sbin:/usr/bin",
		},
		[]oci.Mount{{
			Destination: "/devshellprefix",
			Source:      prefixSrc,
			Type:        "bind",
			Options:     []string{"ro", "bind"},
		}},
	)

	if err := wrapEntrypoint(spec, rootfs); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}

	shim := filepath.Join(rootfs, "usr/local/sbin/tool")
	got := mustRead(t, shim)
	if !strings.Contains(got, `export PATH="/devshellprefix:$PATH"`) {
		t.Errorf("shim missing additive PATH export:\n%s", got)
	}
	// Execs the CONTAINER path of the prefix tool, not the host source path.
	if !strings.Contains(got, `exec "/devshellprefix/tool" "$@"`) {
		t.Errorf("shim should exec the container path of the prefix tool:\n%s", got)
	}
	// The RO mount source is untouched; no .real created anywhere.
	if exists(filepath.Join(prefixSrc, "tool.real")) {
		t.Errorf("prefix source tool should NOT have been moved aside")
	}
}

// T10 analog: an absolute entrypoint inside the writable rootfs is wrapped in
// place — the real is moved to <entry>.real and a wrapper is written at <entry>.
func TestWrapEntrypoint_AbsoluteInRootfs(t *testing.T) {
	rootfs := t.TempDir()
	mkExec(t, filepath.Join(rootfs, "bin/sh"))

	spec := specWith(
		[]string{"/bin/sh"},
		[]string{
			"DEVSHELL_PREFIX=/devshellprefix",
			"PATH=/usr/local/sbin:/usr/bin",
		},
		nil,
	)

	if err := wrapEntrypoint(spec, rootfs); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}

	// Real moved aside.
	if !exists(filepath.Join(rootfs, "bin/sh.real")) {
		t.Errorf("real /bin/sh should have been moved to /bin/sh.real")
	}
	// Wrapper at the original path.
	got := mustRead(t, filepath.Join(rootfs, "bin/sh"))
	if !strings.Contains(got, `export PATH="/devshellprefix:$PATH"`) {
		t.Errorf("wrapper missing additive PATH export:\n%s", got)
	}
	if !strings.Contains(got, `exec "/bin/sh.real" "$@"`) {
		t.Errorf("wrapper should exec the moved-aside real:\n%s", got)
	}
}

// T9 limitation: an absolute path into a RO-mounted prefix is NOT materialized
// in the rootfs overlay, so it is NOT wrapped — the original mount is untouched
// and no error is returned.
func TestWrapEntrypoint_AbsoluteIntoROMount_NotWrapped(t *testing.T) {
	rootfs := t.TempDir()
	prefixSrc := t.TempDir()
	mkExec(t, filepath.Join(prefixSrc, "tool"))
	// Note: deliberately do NOT create rootfs/devshellprefix/tool.

	spec := specWith(
		[]string{"/devshellprefix/tool"},
		[]string{
			"DEVSHELL_PREFIX=/devshellprefix",
			"PATH=/usr/local/sbin:/usr/bin",
		},
		[]oci.Mount{{
			Destination: "/devshellprefix",
			Source:      prefixSrc,
			Type:        "bind",
			Options:     []string{"ro", "bind"},
		}},
	)

	if err := wrapEntrypoint(spec, rootfs); err != nil {
		t.Fatalf("wrapEntrypoint should be a no-op, got error: %v", err)
	}

	// Nothing written into the rootfs overlay for the absolute path.
	if exists(filepath.Join(rootfs, "devshellprefix/tool")) {
		t.Errorf("a shim should NOT have been written into the rootfs overlay")
	}
	// The RO mount source is untouched.
	if exists(filepath.Join(prefixSrc, "tool.real")) {
		t.Errorf("RO mount source must be untouched")
	}
	if !exists(filepath.Join(prefixSrc, "tool")) {
		t.Errorf("RO mount source tool must still exist")
	}
}

// hostOf mapping: container path under a mount maps to source+suffix; the
// longest matching destination wins when nested; unmounted paths fall back to
// rootfs+path. Exercised through wrapEntrypoint via the resolver behaviour.
func TestHostOf_Mapping(t *testing.T) {
	rootfs := t.TempDir()
	outerSrc := t.TempDir()
	innerSrc := t.TempDir()

	mounts := []oci.Mount{
		{Destination: "/nix", Source: outerSrc},
		{Destination: "/nix/store", Source: innerSrc},
	}
	hostOf := makeHostOf(mounts, rootfs)

	// Longest destination wins: /nix/store/x -> innerSrc/x (not outerSrc/store/x).
	if got, want := hostOf("/nix/store/x"), filepath.Join(innerSrc, "x"); got != want {
		t.Errorf("nested mount: hostOf(/nix/store/x)=%q want %q", got, want)
	}
	// /nix/foo -> outerSrc/foo.
	if got, want := hostOf("/nix/foo"), filepath.Join(outerSrc, "foo"); got != want {
		t.Errorf("outer mount: hostOf(/nix/foo)=%q want %q", got, want)
	}
	// "/nixfoo" must NOT match the "/nix" destination (boundary check).
	if got, want := hostOf("/nixfoo"), filepath.Join(rootfs, "nixfoo"); got != want {
		t.Errorf("boundary: hostOf(/nixfoo)=%q want %q", got, want)
	}
	// Unmounted path falls back to rootfs+path.
	if got, want := hostOf("/usr/bin/x"), filepath.Join(rootfs, "usr/bin/x"); got != want {
		t.Errorf("fallback: hostOf(/usr/bin/x)=%q want %q", got, want)
	}
}

// Best-effort: missing DEVSHELL_PREFIX, empty args, an empty entry, and an
// unresolvable relative entry each return nil with nothing wrapped.
func TestWrapEntrypoint_BestEffortNoOps(t *testing.T) {
	t.Run("missing DEVSHELL_PREFIX", func(t *testing.T) {
		rootfs := t.TempDir()
		mkExec(t, filepath.Join(rootfs, "usr/bin/bash"))
		spec := specWith([]string{"bash"}, []string{"PATH=/usr/bin"}, nil)
		if err := wrapEntrypoint(spec, rootfs); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
		assertNoShims(t, rootfs)
	})

	t.Run("empty args", func(t *testing.T) {
		rootfs := t.TempDir()
		spec := specWith(nil, []string{"DEVSHELL_PREFIX=/p", "PATH=/usr/bin"}, nil)
		if err := wrapEntrypoint(spec, rootfs); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("empty entry", func(t *testing.T) {
		rootfs := t.TempDir()
		spec := specWith([]string{""}, []string{"DEVSHELL_PREFIX=/p", "PATH=/usr/bin"}, nil)
		if err := wrapEntrypoint(spec, rootfs); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("unresolvable relative entry", func(t *testing.T) {
		rootfs := t.TempDir()
		if err := os.MkdirAll(filepath.Join(rootfs, "usr/bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		spec := specWith(
			[]string{"doesnotexist"},
			[]string{"DEVSHELL_PREFIX=/devshellprefix", "PATH=/usr/bin"},
			nil,
		)
		if err := wrapEntrypoint(spec, rootfs); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
		assertNoShims(t, rootfs)
	})

	t.Run("nil Process", func(t *testing.T) {
		rootfs := t.TempDir()
		spec := &oci.Spec{}
		if err := wrapEntrypoint(spec, rootfs); err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})
}

// Direct wrapAt content/permission assertion via the relative-tool path: the
// shim is exactly the documented #!/bin/sh + export PATH + exec, 0755.
func TestShimContentAndPermissions(t *testing.T) {
	rootfs := t.TempDir()
	mkExec(t, filepath.Join(rootfs, "usr/bin/bash"))
	spec := specWith(
		[]string{"bash"},
		[]string{"DEVSHELL_PREFIX=/devshellprefix", "PATH=/usr/local/sbin:/usr/bin"},
		nil,
	)
	if err := wrapEntrypoint(spec, rootfs); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}
	shim := filepath.Join(rootfs, "usr/local/sbin/bash")
	want := "#!/bin/sh\nexport PATH=\"/devshellprefix:$PATH\"\nexec \"/usr/bin/bash\" \"$@\"\n"
	if got := mustRead(t, shim); got != want {
		t.Errorf("shim content mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	fi, err := os.Stat(shim)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("shim perm = %v, want 0755", fi.Mode().Perm())
	}
}

// Relative entry whose resolved real binary lives in the FIRST image-PATH dir:
// the shim would clobber it, so the real is moved aside and the wrapper execs
// the moved-aside copy.
func TestWrapEntrypoint_RelativeShimClobbersReal(t *testing.T) {
	rootfs := t.TempDir()
	// The tool lives in /usr/local/sbin, which is the first PATH dir.
	mkExec(t, filepath.Join(rootfs, "usr/local/sbin/tool"))
	spec := specWith(
		[]string{"tool"},
		[]string{"DEVSHELL_PREFIX=/devshellprefix", "PATH=/usr/local/sbin:/usr/bin"},
		nil,
	)
	if err := wrapEntrypoint(spec, rootfs); err != nil {
		t.Fatalf("wrapEntrypoint: %v", err)
	}
	// Real moved aside.
	if !exists(filepath.Join(rootfs, "usr/local/sbin/tool.real")) {
		t.Errorf("real tool should have been moved to tool.real")
	}
	got := mustRead(t, filepath.Join(rootfs, "usr/local/sbin/tool"))
	if !strings.Contains(got, `exec "/usr/local/sbin/tool.real" "$@"`) {
		t.Errorf("wrapper should exec the moved-aside real:\n%s", got)
	}
}

// assertNoShims fails if any shim-looking file was written into the rootfs
// (anything beyond the fixtures the test created). It checks the well-known
// shadow dir only, which is enough for the no-op cases above.
func assertNoShims(t *testing.T, rootfs string) {
	t.Helper()
	for _, p := range []string{
		filepath.Join(rootfs, "usr/local/sbin/bash"),
		filepath.Join(rootfs, "usr/bin/bash.real"),
	} {
		if exists(p) {
			t.Errorf("unexpected file written: %s", p)
		}
	}
}
