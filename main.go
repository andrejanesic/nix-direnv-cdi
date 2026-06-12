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
// and prints the device reference. (PLAN §3 "gen", milestone 2.)
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

	deviceName := fingerprint.Compute(ds.ProjectRoot)
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

	// TODO(milestone 2): resolve the per-mode output dir, write the spec, then
	// print the `--device` reference and the `export DIRENV_CDI=...` line.
	_ = mode
	_ = out
	_ = spec
	return errors.New("gen: spec placement & write not implemented (PLAN milestone 2)")
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
