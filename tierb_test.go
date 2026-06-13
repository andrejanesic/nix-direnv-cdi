package main

// Tier B integration test (PLAN §5 — the synthetic, nix-free end-to-end test).
//
// This is the Go port of the *assertion* half of the proven bash MVP
// (../cdi-additive-test.sh, lines 103-138, 13/13). Unlike the M4 integration
// test (integration_test.go), it needs NO nix and NO direnv: it fabricates a
// one-file fake dev-shell prefix at runtime, builds a real CDI spec for it via
// our library (cdispec.Build + Write), and drives real podman to assert the
// full T1-T10 matrix. The whole point is that it runs on a bare CI runner with
// only a container runtime present.
//
// It REUSES the helpers/consts declared in integration_test.go (same package):
// chmodTraversable, podman, podmanRunNoDevice, build, cmdTimeout. It adds only
// the debianImage const and the podmanRunDeviceEntrypoint helper (the device
// runner is open-coded since the matrix mixes device/no-device/entrypoint
// cases).
//
// Skip conditions: testing.Short(); or podman missing from PATH. NO nix/direnv
// requirement (that is the whole point of Tier B).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
)

const (
	// debianImage is the Tier B base image. It is NOT busyboxImage because
	// T1/T4/T10 exercise real `bash`, which busybox lacks. --network=none keeps
	// every run hermetic; the first run pulls this image.
	debianImage = "debian:bookworm-slim"

	// prefixToolMarker is what the fake dev-shell-only tool prints when it runs.
	prefixToolMarker = "PREFIXTOOL-RAN"
	// prefixToolContent is EXACTLY the MVP's synthetic prefixtool (lines 18-21).
	// A pure `#!/bin/sh` script (no libraries) runs with just the base image's
	// /bin/sh — that is what keeps this test nix-free.
	prefixToolContent = "#!/bin/sh\necho PREFIXTOOL-RAN\necho \"toolPATH=$PATH\"\n"
)

// TestTierB_SyntheticDevShell ports the MVP's T1-T10 matrix with a fabricated
// (nix-free) dev-shell prefix and a real CDI spec built by our library.
func TestTierB_SyntheticDevShell(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Tier B integration test in -short mode")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("Tier B integration test requires %q on PATH (not found): %v", "podman", err)
	}
	// docker here resolves to a podman shim; the docker CDI path needs
	// daemon-registered spec dirs (no per-run --cdi-spec-dir), so we detect and
	// log it but do not exercise it (mirrors the M4 test's decision).
	if _, err := exec.LookPath("docker"); err == nil {
		t.Log("docker present but docker CDI path deferred (needs daemon-registered spec dirs); podman only for Tier B")
	}

	// 1. Fabricate the fake dev-shell prefix: work/prefix/prefixtool with the
	//    MVP's exact content, chmod 0755. No nix involved.
	work := t.TempDir()
	prefixDir := filepath.Join(work, "prefix")
	if err := os.MkdirAll(prefixDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", prefixDir, err)
	}
	toolPath := filepath.Join(prefixDir, "prefixtool")
	if err := os.WriteFile(toolPath, []byte(prefixToolContent), 0o755); err != nil {
		t.Fatalf("write %s: %v", toolPath, err)
	}
	// WriteFile honours umask; force the exec bits explicitly.
	if err := os.Chmod(toolPath, 0o755); err != nil {
		t.Fatalf("chmod %s 0755: %v", toolPath, err)
	}

	// 2. Build the hook binary into a TempDir. The generated spec embeds this
	//    path as the createRuntime hook, so it must persist for the whole test
	//    (a TempDir does). Reuse the M4 build helper.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "nix-direnv-cdi")
	build(t, bin)

	// 3. Build the spec via our library — NOT via gen/direnv (which need a real
	//    .direnv). cdispec.Build mounts the closure at source==dest, so both
	//    DEVSHELL_PREFIX and the in-container prefix dir are the HOST prefixDir
	//    path. Every assertion below therefore uses prefixDir, not a hardcoded
	//    /devshellprefix.
	//      - ProjectRoot: work        -> mounted rw (source==dest)
	//      - Closure:     [prefixDir] -> mounted ro (source==dest); makes
	//                                    prefixtool visible in-container at
	//                                    prefixDir.
	//      - Prefix:      [prefixDir] -> DEVSHELL_PREFIX=prefixDir (additive).
	//      - Env:         MARKER=1    -> a harmless propagated var.
	ds := &devshell.DevShell{
		ProjectRoot: work,
		Prefix:      []string{prefixDir},
		Closure:     []string{prefixDir},
		Env:         map[string]string{"MARKER": "1"},
	}
	spec, err := cdispec.Build(ds, "tierb", bin)
	if err != nil {
		t.Fatalf("cdispec.Build: %v", err)
	}
	specDir := t.TempDir()
	if err := cdispec.Write(spec, specDir, "tierb"); err != nil {
		t.Fatalf("cdispec.Write: %v", err)
	}

	// 4. Traversability: rootless podman/crun must traverse the mount sources
	//    (prefixDir/work), the spec dir, and the hook binary's dir, all >=0755.
	//    t.TempDir() is 0700; widen each chain up to /tmp. (PLAN §1; MVP line 9.)
	chmodTraversable(t, prefixDir)
	chmodTraversable(t, work)
	chmodTraversable(t, specDir)
	chmodTraversable(t, binDir)

	// 5. Device ref: cdispec uses kind "nix-direnv.cdi/shell"; our device name
	//    is "tierb". Assert it is well-formed (matches what gen would print).
	ref := cdispec.Kind + "=" + "tierb"
	if ref != "nix-direnv.cdi/shell=tierb" {
		t.Fatalf("device ref = %q, want nix-direnv.cdi/shell=tierb", ref)
	}
	t.Logf("device ref: %s", ref)
	t.Logf("synthetic prefix dir (DEVSHELL_PREFIX): %s", prefixDir)

	// prefix is the actual host prefixDir path — what DEVSHELL_PREFIX carries
	// and where prefixtool lives in-container (source==dest). All PATH/run
	// assertions key off this, NOT the MVP's /devshellprefix.
	prefix := prefixDir

	// device runs a `podman run --device` with the Tier B device attached and
	// no extra entrypoint (entrypoint comes from args[0]).
	device := func(args ...string) string {
		full := append([]string{
			"run", "--rm", "--network=none",
			"--cdi-spec-dir", specDir, "--device", ref, debianImage,
		}, args...)
		return podman(t, full...)
	}

	// T1: entrypoint=bash, PATH is ADDITIVE (prefix prepended, base preserved).
	t.Run("T1_additive_path_bash", func(t *testing.T) {
		out := device("bash", "-c", `echo "PATH=$PATH"`)
		if !strings.Contains(out, "PATH="+prefix+":") {
			t.Errorf("T1: prefix not first in PATH\nwant substring: %q\ngot:\n%s", "PATH="+prefix+":", out)
		}
		if !strings.Contains(out, ":/usr/bin") {
			t.Errorf("T1: image base /usr/bin not preserved in PATH\ngot:\n%s", out)
		}
	})

	// T2: a dev-shell-only tool is reachable via the additive PATH.
	t.Run("T2_prefixtool_reachable", func(t *testing.T) {
		out := device("bash", "-c", "prefixtool")
		if !strings.Contains(out, prefixToolMarker) {
			t.Errorf("T2: prefixtool not found/run via additive PATH\nwant: %q\ngot:\n%s", prefixToolMarker, out)
		}
	})

	// T3: base-image tools still resolve (additive, not overriding).
	t.Run("T3_base_tool_still_works", func(t *testing.T) {
		out := device("bash", "-c", "cat /etc/hostname >/dev/null && echo BASE-CAT-OK")
		if !strings.Contains(out, "BASE-CAT-OK") {
			t.Errorf("T3: base `cat` failed with the device attached\ngot:\n%s", out)
		}
	})

	// T4: the wrapped entrypoint actually execs the REAL bash.
	t.Run("T4_real_bash_execs", func(t *testing.T) {
		out := device("bash", "-c", "echo HELLO-FROM-REAL-BASH")
		if !strings.Contains(out, "HELLO-FROM-REAL-BASH") {
			t.Errorf("T4: real bash did not run through the wrapper\ngot:\n%s", out)
		}
	})

	// T5: works for a different entrypoint (sh) — additive, no shebang recursion
	//     hang. sh is wrapped via the relative shadow shim; the shim's own
	//     #!/bin/sh resolves to the untouched image /bin/sh.
	t.Run("T5_sh_entrypoint_additive", func(t *testing.T) {
		out := podmanRunDeviceEntrypoint(t, specDir, ref, "sh", debianImage,
			"-c", `echo "PATH=$PATH"`)
		if !strings.Contains(out, "PATH="+prefix+":") {
			t.Errorf("T5: sh not wrapped additively (or recursion hang)\nwant substring: %q\ngot:\n%s", "PATH="+prefix+":", out)
		}
	})

	// T6: control — WITHOUT the device, PATH is the plain image default (no
	//     prefix leak). Reuse the M4 podmanRunNoDevice helper.
	t.Run("T6_control_no_device", func(t *testing.T) {
		out := podmanRunNoDevice(t, debianImage, "bash", "-c", `echo "PATH=$PATH"`)
		if strings.Contains(out, prefix) {
			t.Errorf("T6: prefix leaked into PATH WITHOUT the device\nunwanted substring: %q\ngot:\n%s", prefix, out)
		}
	})

	// T7: a dev-shell-only tool as the BARE (relative) entrypoint runs — prefix
	//     resolution + shadow shim in the first image-PATH dir.
	t.Run("T7_bare_prefixtool_entrypoint", func(t *testing.T) {
		out := device("prefixtool")
		if !strings.Contains(out, prefixToolMarker) {
			t.Errorf("T7: bare prefixtool entrypoint did not run\nwant: %q\ngot:\n%s", prefixToolMarker, out)
		}
	})

	// T8: additive PATH holds even when the entry resolves to a dev-shell-only
	//     tool (sh -c runs prefixtool, then echoes PATH).
	t.Run("T8_additive_path_via_prefixtool", func(t *testing.T) {
		out := device("sh", "-c", `prefixtool && echo "PATH=$PATH"`)
		if !strings.Contains(out, prefix) {
			t.Errorf("T8: prefix absent from PATH\nwant substring: %q\ngot:\n%s", prefix, out)
		}
		if !strings.Contains(out, ":/usr/bin") {
			t.Errorf("T8: image base /usr/bin not preserved in PATH\ngot:\n%s", out)
		}
	})

	// T9: LIMITATION — an ABSOLUTE path into the RO-mounted prefix runs (the
	//     mount makes it present), but PATH is NOT made additive: the binary is
	//     in a RO mount, not the writable rootfs overlay, so the hook's
	//     existence check fails and it is left intact (no wrap). Assert it runs
	//     (PREFIXTOOL-RAN) but its emitted toolPATH does NOT carry the prefix.
	t.Run("T9_absolute_ro_mount_not_additive", func(t *testing.T) {
		out := device(filepath.Join(prefix, "prefixtool"))
		if !strings.Contains(out, prefixToolMarker) {
			t.Errorf("T9: absolute path into the RO mount did not run\nwant: %q\ngot:\n%s", prefixToolMarker, out)
		}
		if strings.Contains(out, "toolPATH="+prefix) {
			t.Errorf("T9: PATH was unexpectedly made additive for the RO-mount absolute path\nunwanted substring: %q\ngot:\n%s", "toolPATH="+prefix, out)
		}
	})

	// T10: an ABSOLUTE path into the WRITABLE image rootfs (/bin/bash) DOES get
	//      wrapped in place -> additive.
	t.Run("T10_absolute_rootfs_additive", func(t *testing.T) {
		out := podmanRunDeviceEntrypoint(t, specDir, ref, "/bin/bash", debianImage,
			"-c", `echo "PATH=$PATH"`)
		if !strings.Contains(out, "PATH="+prefix+":") {
			t.Errorf("T10: absolute image-rootfs entrypoint not additive\nwant substring: %q\ngot:\n%s", "PATH="+prefix+":", out)
		}
	})
}

// podmanRunDeviceEntrypoint runs the Tier B device with an explicit
// --entrypoint injected before the image (T5/T10 need this: the entrypoint is
// not args[0]). Returns combined output; reuses podman (non-fatal on non-zero).
func podmanRunDeviceEntrypoint(t *testing.T, specDir, ref, entrypoint, image string, args ...string) string {
	t.Helper()
	full := append([]string{
		"run", "--rm", "--network=none",
		"--cdi-spec-dir", specDir, "--device", ref,
		"--entrypoint", entrypoint, image,
	}, args...)
	return podman(t, full...)
}
