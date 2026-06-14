# Contributing

Thanks for taking the time to contribute to nix-direnv-cdi.

## License

This project is licensed under the Apache License, Version 2.0. By submitting a
contribution, you agree that your contribution is licensed under the same
Apache-2.0 terms, unless you explicitly state otherwise in writing.

Copyright 2026 Andreja Nesic.

Legal/contact email: office@andrejanesic.com.

## Verification

Run the default checks before sending changes:

```sh
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

Select the container CLI with `NDC_CONTAINER_CLI`:

```sh
NDC_CONTAINER_CLI=docker go test ./integration -run '^TestSynthetic'
NDC_CONTAINER_CLI=docker go test ./integration -run '^TestE2E'
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

## Hook Debugging

`createRuntime` hook output is normally hidden by the container runtime. Set
`NDC_HOOK_LOG=<file>` in the launching environment to append a hook trace with
the gate decision, mounts, and `DIRENV_DIFF` decoding.
