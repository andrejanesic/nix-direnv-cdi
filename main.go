// Command nix-direnv-cdi makes a project's nix-direnv dev-shell available
// inside any OCI container (podman, docker) via one generic CDI device whose
// createRuntime hook injects the dev-shell dynamically at run time. See docs/
// (start at docs/readme.md) for the full design.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrejanesic/nix-direnv-cdi/internal/cdispec"
	"github.com/andrejanesic/nix-direnv-cdi/internal/devshell"
	"github.com/andrejanesic/nix-direnv-cdi/internal/hook"
	"github.com/andrejanesic/nix-direnv-cdi/internal/install"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `nix-direnv-cdi — expose a nix-direnv dev-shell inside any OCI container via a CDI device.

Usage:
  nix-direnv-cdi <command> [flags]

Commands:
  gen        Write the project's dev-shell closure to .direnv/cdi/mounts.json
  hook       createRuntime hook: inject the dev-shell into the container (best-effort)
  install    Register the generic CDI device with podman/docker (one-time)
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

// cmdGen computes the project's dev-shell closure and writes it to
// <project>/.direnv/cdi/mounts.json (the data the runtime hook bind-mounts),
// then reports the constant device reference to attach. It needs only the
// gcroot (not DIRENV_DIFF), so it is safe to run inside `.envrc` right after
// `use flake`.
func cmdGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	out := fs.String("out", "", "output dir for mounts.json (default: <project>/.direnv/cdi)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := devshell.ProjectRoot()
	if err != nil {
		return err
	}
	closure, err := devshell.Closure(root)
	if err != nil {
		return err
	}

	dir := *out
	if dir == "" {
		dir = filepath.Join(root, ".direnv", "cdi")
	}
	path := filepath.Join(dir, "mounts.json")
	if err := devshell.WriteMounts(path, closure); err != nil {
		return err
	}

	// Status to stderr; gen has no stdout. The device reference is the constant
	// cdispec.Ref — the same for every project.
	fmt.Fprintf(os.Stderr,
		"nix-direnv-cdi: wrote %d closure paths -> %s\n"+
			"  attach with: podman run --device %s <image> <cmd>\n",
		len(closure), path, cdispec.Ref)
	return nil
}

// sharedSpecDir resolves the shared CDI spec directory: $XDG_CONFIG_HOME/cdi,
// falling back to ~/.config/cdi.
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
// container.
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

// cmdInstall writes the single generic CDI device to the shared CDI dir (its
// hook path = this installed binary) and registers that dir with podman and
// docker so `--device nix-direnv-cdi.org/env=current` resolves. Run once per
// machine. Best-effort registration: per-runtime failures fall back to printed
// manual instructions.
func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	spec, err := cdispec.Build(self)
	if err != nil {
		return err
	}
	sharedDir, err := sharedSpecDir()
	if err != nil {
		return err
	}
	// Write validates the spec and makes the dir traversable.
	if err := cdispec.Write(spec, sharedDir); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote generic CDI device (%s) -> %s\n",
		cdispec.Ref, filepath.Join(sharedDir, cdispec.FileName))

	paths, err := install.DefaultPaths(sharedDir)
	if err != nil {
		return err
	}
	return install.Run(paths, os.Stdout)
}
