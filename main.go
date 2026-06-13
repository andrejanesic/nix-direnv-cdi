// Command nix-direnv-cdi makes a project's nix-direnv dev-shell available
// inside any OCI container (podman, docker, docker compose) by generating a CDI
// device: read-only closure mounts + dev-shell env + a createRuntime hook that
// makes PATH additive. See PLAN.md for the full design.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
	"github.com/andrejanesic/nix-direnv-cdi/internal/fingerprint"
	"github.com/andrejanesic/nix-direnv-cdi/internal/hook"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `nix-direnv-cdi — expose a nix-direnv dev-shell inside any OCI container via a CDI device.

Usage:
  nix-direnv-cdi <command> [flags]

Commands:
  gen        Discover the dev-shell and write a validated CDI spec
  hook       createRuntime hook: wrap the entrypoint for additive PATH (best-effort)
  install    Register the shared CDI spec dir with podman/docker (idempotent)
  version    Print version information

Run "nix-direnv-cdi <command> -h" for command-specific flags.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "gen":
		exitOnErr(cmdGen(args))
	case "hook":
		// Best-effort by contract: a hook must never break the container, so
		// cmdHook reports errors but the process always exits 0.
		cmdHook(args)
	case "install":
		exitOnErr(cmdInstall(args))
	case "version":
		fmt.Printf("nix-direnv-cdi %s\n", version)
	case "-h", "--help", "help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s\n", cmd, usage)
		os.Exit(2)
	}
}

func exitOnErr(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "nix-direnv-cdi:", err)
		os.Exit(1)
	}
}

// cmdGen discovers the dev-shell, builds and validates the CDI spec, writes it,
// and prints the device reference. (PLAN §3 "gen", milestones 2 & 5.)
//
// Placement differs by --mode (see resolvePlacement):
//   - shared: ~/.config/cdi, device name = fingerprint(projectRoot); registered.
//   - local:  $PWD/.direnv/cdi, constant device name "shell"; gitignored,
//     unregistered, podman-only.
//
// stdout is identical in shape for both modes (line 1 = bare device ref, line 2
// = the eval-able export) so `--device "$DIRENV_CDI"` works uniformly. local
// mode adds an extra stderr hint with the --cdi-spec-dir to pass.
func cmdGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	mode := fs.String("mode", "shared", "spec placement: shared|local")
	out := fs.String("out", "", "output directory (default: per-mode)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ds, err := devshell.Discover()
	if err != nil {
		return err
	}

	dir, deviceName, err := resolvePlacement(*mode, *out, ds.ProjectRoot, sharedSpecDir)
	if err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}

	spec, err := cdispec.Build(ds, deviceName, self)
	if err != nil {
		return err
	}
	if err := cdispec.Validate(spec); err != nil {
		return err
	}
	if err := cdispec.Write(spec, dir, deviceName); err != nil {
		return err
	}

	ref := cdispec.Kind + "=" + deviceName
	specPath := filepath.Join(dir, "nix-direnv-"+deviceName+".json")
	// Human-readable status to stderr so stdout stays eval-clean.
	fmt.Fprintf(os.Stderr, "wrote CDI spec for %s -> %s\n", ds.ProjectRoot, specPath)
	if *mode == "local" {
		// local is unregistered and podman-only, so the user must pass the spec
		// dir explicitly. Emit the absolute path as a copy-pasteable hint.
		absDir, aerr := filepath.Abs(dir)
		if aerr != nil {
			absDir = dir
		}
		fmt.Fprintf(os.Stderr,
			"local mode (podman-only): attach with  podman run --cdi-spec-dir %s --device \"$DIRENV_CDI\" <image> <cmd>\n",
			absDir)
	}
	// stdout: the device reference and the eval-able export line.
	fmt.Println(ref)
	fmt.Printf("export DIRENV_CDI=%s\n", ref)
	return nil
}

// resolvePlacement maps (mode, out, projectRoot) to the spec output directory
// and CDI device name, without any I/O beyond the injected sharedDir resolver.
// It is the single source of truth for the shared-vs-local placement decision
// (PLAN §2) and is unit-tested directly.
//
//   - shared: dir = sharedDir() (or out if non-empty); name = fingerprint.
//   - local:  dir = <projectRoot>/.direnv/cdi (or out if non-empty); name = "shell".
//   - other:  error.
//
// An explicit --out always overrides the per-mode default directory; the device
// name is never affected by --out.
func resolvePlacement(mode, out, projectRoot string, sharedDir func() (string, error)) (dir, deviceName string, err error) {
	switch mode {
	case "shared":
		deviceName = fingerprint.Compute(projectRoot)
		if out != "" {
			return out, deviceName, nil
		}
		dir, err = sharedDir()
		if err != nil {
			return "", "", err
		}
		return dir, deviceName, nil
	case "local":
		deviceName = cdispec.Class // the constant device name "shell"
		if out != "" {
			return out, deviceName, nil
		}
		return filepath.Join(projectRoot, ".direnv", "cdi"), deviceName, nil
	default:
		return "", "", fmt.Errorf("unknown --mode %q (want shared|local)", mode)
	}
}

// sharedSpecDir resolves the shared CDI spec directory: $XDG_CONFIG_HOME/cdi,
// falling back to ~/.config/cdi. (PLAN §2 shared placement.)
func sharedSpecDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cdi"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".config", "cdi"), nil
}

// cmdHook runs the createRuntime hook. It is best-effort: any error is reported
// to stderr but the process still exits 0, so a failure never breaks the
// container. (PLAN §1, §3 "hook", milestone 3.)
func cmdHook(args []string) {
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "nix-direnv-cdi hook:", err)
		return
	}
	if err := hook.Run(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "nix-direnv-cdi hook (ignored):", err)
	}
}

// cmdInstall registers the shared CDI spec dir with podman/docker. (PLAN §3
// "install", optional, milestone 7.)
func cmdInstall(args []string) error {
	return errors.New("install: not implemented (PLAN milestone 7)")
}
