# Contributing and Maintaining

Thanks for taking the time to contribute to nix-direnv-cdi.

This file is the human operating guide for changes to the project: how to
verify them, what invariants reviewers must preserve, how to debug hook
failures, and what maintainers must check before changing support claims or
cutting releases. For lower-level mechanism details, start with
[docs/internals.md](docs/internals.md) and
[docs/mechanisms.md](docs/mechanisms.md).

## License

This project is licensed under the Apache License, Version 2.0. By submitting a
contribution, you agree that your contribution is licensed under the same
Apache-2.0 terms, unless you explicitly state otherwise in writing.

Copyright 2026 Andreja Nesic.

Legal/contact email: office@andrejanesic.com.

## PR checklist

A change is ready to merge when all of the following hold. Each item links to
the section with the detail.

1. **Formatting is clean** — `gofmt -l .` reports nothing (CI-enforced). See
   [Verification](#verification).
2. **Build and vet pass** — `go build ./...` and `go vet ./...` succeed. See
   [Verification](#verification).
3. **The full test suite is green** — `go test ./...` passes in CI (unit plus
   the integration, e2e, and installer suites for the selected container CLI)
   before merge. Missing prerequisites are failures, not skips, and any
   deliberately skipped suite is named in the PR. Locally you need only run the
   scope in [Validation Policy](#validation-policy); CI runs the rest. See also
   [Test Suites](#test-suites).
4. **Tests cover the change** — new tests are added, or existing tests updated,
   to exercise the new or changed behavior.
5. **The Review Checklist for each changed package is satisfied.** See
   [Review Checklist](#review-checklist).
6. **Project Invariants are preserved** — or the design is being changed
   intentionally and the docs/tests move with it. See
   [Project Invariants](#project-invariants).
7. **Runtime support-claim changes carry fresh docker/podman e2e evidence**
   before merging. See [Validation Policy](#validation-policy).
8. **Docs and notes are updated** when the change touches commands, behavior,
   support claims, or release policy — including `CHANGELOG.md` and release
   notes for support or installer changes.
9. **Docs tables of contents are updated** when a doc is added, renamed, or
   removed — update every affected TOC (`docs/readme.md`, the README
   Documentation list, and the AGENTS.md repo map).
10. **Release preparation follows [docs/release.md](docs/release.md)** when
    cutting a release.

## Verification

Run the default checks before sending changes:

```sh
gofmt -l .
go build ./...
go vet ./...
go test ./...
```

`go test ./...` runs the unit tests plus the synthetic and e2e integration
tests for the selected container CLI. The default CLI is `docker`. Missing
suite prerequisites are test failures.

For a unit-only check, omit the integration suites with `-skip`:

```sh
go test ./... -skip '^(TestSynthetic|TestE2E)'
```

If Go cannot write its default build cache in a restricted environment, point it
at a writable directory:

```sh
GOCACHE=/tmp/go-build-cache go test ./...
```

If a non-standard worktree layout prevents Go from resolving VCS status, add
`-buildvcs=false` to the build command:

```sh
go build -buildvcs=false ./...
```

## Test Suites

| Suite | Command | Requires |
|---|---|---|
| unit | `go test ./... -skip '^(TestSynthetic|TestE2E)'` | Go |
| synthetic | `go test ./integration -run '^TestSynthetic'` | selected container CLI + `busybox` image |
| e2e | `go test ./integration -run '^TestE2E'` | selected container CLI + `busybox` image + nix + direnv |
| installer lifecycle | `go test ./... -run 'Install|Uninstall|Registration'` | Go, and podman for registration tests |
| package | `nix build .#nix-direnv-cdi` | nix |

Select the container CLI with `NDC_CONTAINER_CLI`:

```sh
NDC_CONTAINER_CLI=docker go test ./integration -run '^TestSynthetic'
NDC_CONTAINER_CLI=docker go test ./integration -run '^TestE2E'
NDC_CONTAINER_CLI=podman go test ./integration -run '^TestSynthetic'
NDC_CONTAINER_CLI=podman go test ./integration -run '^TestE2E'
```

Docker discovers CDI specs from configured spec directories. The tests write to
`/etc/cdi` by default when `NDC_CONTAINER_CLI=docker`; override that with
`NDC_DOCKER_CDI_SPEC_DIR` if your daemon is configured for a different writable
CDI spec directory. Docker integration tests pass `DIRENV_DIR` and
`DIRENV_DIFF` through to the OCI process env so daemon-driven hooks can still
find the loaded dev-shell context.

For local Docker runs, make the daemon-visible CDI spec directory writable once:

```sh
sudo mkdir -p /etc/cdi
sudo chown "$USER:$(id -gn)" /etc/cdi
```

Docker must also have CDI enabled and include that directory in
`cdi-spec-dirs`. The CI workflow configures this before running Docker
integration.

Use `-skip` when you intentionally cannot run a suite:

```sh
go test ./... -skip '^TestSynthetic'
go test ./... -skip '^TestE2E'
go test ./... -skip '^(TestSynthetic|TestE2E)'
```

## Validation Policy

This policy governs what *you* run locally while iterating — use the narrowest
check that covers the risk. It does not relax the merge gate: CI runs the full
suite (`go test ./...`) and it must be green before merge (see
[PR checklist](#pr-checklist) item 3). Do not rely only on unit tests for
runtime behavior:

- Pure unit-level changes: run unit tests.
- `internal/hook`, `internal/nsmount`, or `internal/cdispec` changes: run unit
  tests plus real container validation for the affected runtime. For shared
  behavior, validate both docker and podman.
- `internal/devshell` changes: run unit tests. If the change affects generated
  closures, `DIRENV_DIFF`, `PATH`, or exported env behavior, also run e2e.
- `internal/install` changes: run installer lifecycle tests and any affected
  runtime smoke/registration tests.
- Documentation-only changes: run no code tests unless the docs change support
  claims, command behavior, release policy, or runtime coverage.
- Runtime support-claim changes for docker or podman require fresh e2e evidence
  from that runtime before merging.
- Release preparation must follow [docs/release.md](docs/release.md), including
  package checks and version-output verification.

Missing integration prerequisites are failures by design. Use `-skip` only when
intentionally omitting a suite, and say what was skipped in the review or
release notes.

## Project Invariants

Abide these rules unless the design is intentionally being changed and the
docs/tests move with it.

- The hook must always exit 0; a non-zero `createRuntime` hook aborts the
  container.
- `DIRENV_DIR` is the authorization gate. Without it, the device is inert.
- `gen` must not depend on `DIRENV_DIFF`; it derives the closure from the
  gcroot so it can run during `.envrc` evaluation.
- The hook reads `DIRENV_DIFF` at container run time and resolves direnv context
  from the hook environment first, then OCI `process.env` for daemon-driven
  CLIs such as Docker.
- Mount injection is best-effort. Mount failures must be logged/debuggable but
  must not abort the container or prevent entrypoint wrapping.
- `nsmount` must call `unshare(CLONE_FS)` before `setns(CLONE_NEWNS)`, on a
  locked OS thread that is discarded after namespace entry.
- Closure paths can be files or directories; bind targets must match the source
  type.
- Read-only remount is best-effort under rootless user namespaces.
- The CDI spec must stay generic: no project path, closure, or environment data
  belongs in the device.
- The CDI spec dir, hook binary, and `.direnv/cdi/mounts.json` path chain must
  stay traversable for rootless runtimes.
- Entrypoint wrapping must preserve the T1-T10 behavior matrix in
  [docs/mechanisms.md](docs/mechanisms.md), including the T9 limitation for
  absolute paths into read-only store mounts.

## Review Checklist

For `internal/hook` changes, check:

- no `DIRENV_DIR` still means no mounts, no wrapping, and no container failure
- hook errors cannot make the hook process fail the container
- mount failures are visible through `NDC_HOOK_LOG` and ignored by normal flow
- hook-env and OCI `process.env` fallback behavior still works
- `DIRENV_DIFF` decode errors and missing diffs are handled deliberately
- additive `PATH`, dev-shell env export, and image-tool preservation still match
  the T1-T10 matrix

For `internal/nsmount` changes, check:

- `CLONE_FS` is unshared before entering the mount namespace
- namespace entry remains confined to a locked, discarded OS thread
- file and directory closure paths both bind correctly
- rootless `EPERM` on read-only remount remains non-fatal
- the caller still receives useful errors for debugging while the hook degrades
  gracefully

For `internal/devshell` changes, check:

- closure generation still uses the gcroot and remains usable from `.envrc`
- padded and unpadded `DIRENV_DIFF` encodings still decode
- additive `PATH` prefix derivation excludes previous PATH entries
- exported env excludes `PATH` and `DIRENV_*`
- `.direnv/cdi/mounts.json` remains minimal project data: only the closure

For `internal/cdispec` changes, check:

- the CDI spec validates through the CDI library
- the device remains `nix-direnv-cdi.org/env=current`
- the only per-device edit remains the generic `createRuntime` hook
- the hook path points at the installed binary and remains traversable

For `internal/install` changes, check:

- install owns only the shared CDI spec, the podman drop-in, and the Docker
  system CDI spec
- divergent existing files are backed up before rewrite
- matching existing files are idempotent no-ops
- uninstall removes only owned files and is idempotent
- Docker behavior uses the daemon-scanned system CDI spec path and does not
  silently rewrite unrelated daemon configuration

For docs changes, check:

- command examples match the CLI and tests
- podman/docker support claims match recent runtime validation
- limitations and security claims match the implementation

## Hook Debugging

`createRuntime` hook output is normally hidden by the container runtime. Set
`NDC_HOOK_LOG=<file>` in the launching environment to append a hook trace with
the gate decision, mounts, and `DIRENV_DIFF` decoding.

Typical podman probe:

```sh
NDC_HOOK_LOG=/tmp/ndc-hook.log \
  podman run --rm --device nix-direnv-cdi.org/env=current busybox sh -c 'echo "$PATH"'
cat /tmp/ndc-hook.log
```

For Docker, pass the direnv context through the daemon boundary:

```sh
NDC_HOOK_LOG=/tmp/ndc-hook.log \
  docker run --rm --device nix-direnv-cdi.org/env=current \
  --env DIRENV_DIR --env DIRENV_DIFF --env NDC_HOOK_LOG \
  busybox sh -c 'echo "$PATH"'
cat /tmp/ndc-hook.log
```

Read the log as a triage flow:

- `gate closed`: `DIRENV_DIR` was not visible. Check that the command was run
  from a loaded direnv shell, or for Docker that `--env DIRENV_DIR` was passed.
- `gate open`: the device is authorized for the current project.
- `mounts.json` read errors or zero paths: run `nix-direnv-cdi gen`, check the
  project path from `DIRENV_DIR`, and check rootless traversability of the
  `.direnv/cdi/mounts.json` path chain.
- `mount FAILED`: inspect namespace permission/runtime issues. Rootless setups
  must still allow the hook to enter the container mount namespace.
- `mount OK`: the closure was injected.
- `runtimeEnv ... has=false`: `DIRENV_DIFF` was missing. For Docker, pass
  `--env DIRENV_DIFF`; for podman, confirm the launch shell has direnv loaded.
- `runtimeEnv ... err=...`: the hook saw `DIRENV_DIFF` but could not decode it.
- A dev-shell tool runs but `PATH` is not additive when the entrypoint is an
  absolute `/nix/store/...` path: this is the documented T9 limitation.

Prefer synthetic integration when isolating hook and runtime behavior without
nix/direnv. Move to e2e when the failure depends on real gcroots, `direnv`, or
the committed flake fixture.

## Releases

Release policy, artifact verification, upgrade/rollback instructions, and the
release checklist live in [docs/release.md](docs/release.md). Tagged releases
use SemVer tags such as `v0.1.0`, publish Nix install paths plus standalone
Linux binaries, and include checksums, cosign keyless signatures, and GitHub
artifact provenance.

Do not change runtime support claims without fresh docker and/or podman e2e
evidence, and call out runtime support or installer behavior changes in
`CHANGELOG.md` and release notes.
