# Example: run a coding agent in a container with your pinned toolchain

A complete, copy-pasteable project that boxes the **Codex** coding agent in a
throwaway container while giving it your *real, pinned* project toolchain — no
per-repo agent image to build. The agent and the toolchain both come from this
project's nix-direnv dev-shell; only its `/nix/store` closure is shared into the
container, read-only.

If you haven't installed the tool yet, do that first: see
[../docs/usage.md](../docs/usage.md). This page assumes
`nix-direnv-cdi install` has been run once on the machine.

## Run it

### 1. Enter the project

```sh
cd example
direnv allow
```

This loads the dev-shell (`codex` and `go` are now on your host `PATH`) and runs
`nix-direnv-cdi gen`, writing `.direnv/cdi/mounts.json`. Confirm you're in the
shell — the gate depends on it:

```sh
echo "$DIRENV_DIR"     # must be non-empty
```

### 2. Sanity-check the device

```sh
podman run --rm --device nix-direnv-cdi.org/env=current ubuntu codex --version
```

`codex` isn't in the `ubuntu` image — it resolves because the hook mounted the
dev-shell closure and put its `bin` on `PATH`.

### 3. Run the agent against your sources (podman)

Mount the project read-write so the agent can edit it, set the workdir, and let
Codex loose:

```sh
podman run --rm -it \
  --device nix-direnv-cdi.org/env=current \
  -v "$PWD:$PWD" -w "$PWD" \
  -e OPENAI_API_KEY \
  ubuntu \
  codex "add a hello-world Go program and run it"
```

Inside the container the agent has `codex` *and* `go` from your pinned dev-shell,
operating on your mounted sources — but it can't reach the rest of your machine,
and it can't mutate your toolchain (the store is read-only).

### 4. Same thing on Docker

Docker runs through a daemon that doesn't inherit your shell's direnv
environment, so pass the two bookkeeping variables through explicitly:

```sh
docker run --rm -it \
  --env DIRENV_DIR --env DIRENV_DIFF \
  --device nix-direnv-cdi.org/env=current \
  -v "$PWD:$PWD" -w "$PWD" \
  -e OPENAI_API_KEY \
  ubuntu \
  codex "add a hello-world Go program and run it"
```

(The wrapper unsets `DIRENV_DIR`/`DIRENV_DIFF` before exec'ing the real command,
so the agent doesn't see them.)

## How it works

The flow, end to end:

```
flake.nix ──use flake──▶ loaded dev-shell ──nix-direnv-cdi gen──▶ .direnv/cdi/mounts.json
   (codex + go)           (DIRENV_DIR set)                              │
                                                                        ▼
        podman run --device nix-direnv-cdi.org/env=current  ◀── reads mounts.json + your live env
                                   │
                                   ▼
        createRuntime hook: bind-mounts the closure into the container (read-only),
        prepends the dev-shell's bin dirs to PATH  ──▶  codex + go now exist inside a stock image
```

The files that drive it:

| File | Role |
|------|------|
| `flake.nix` / `flake.lock` | Define the dev-shell: pinned `codex` (the agent) + `go` (stand-in for your toolchain). `flake.lock` pins nixpkgs for reproducibility. |
| `shell.nix` | The same dev-shell for projects that use `use nix` instead of `use flake`. |
| `.envrc` | `use flake` loads the dev-shell; `nix-direnv-cdi gen` records its closure. |
| `.direnv/cdi/mounts.json` | *Generated* by `gen` — the list of `/nix/store` paths in the dev-shell's closure that the hook will mount. |

Two things make this work and are worth keeping in mind:

- **Being in the loaded dev-shell is the authorization.** The hook only acts when
  it sees `DIRENV_DIR` (you ran `direnv allow` and are in the project). Run the
  same `podman run` from anywhere else and the device is **inert** — it mounts
  nothing. See [../docs/security.md](../docs/security.md).
- **The device is generic and constant.** The reference is always
  `nix-direnv-cdi.org/env=current`; nothing about this project is baked into it.
  The launching shell decides which dev-shell, at run time.

## Notes & gotchas

- **Sources aren't mounted for you.** The `-v "$PWD:$PWD"` is what lets the agent
  see and edit your code. Drop it for a read-only/inspection run.
- **Secrets.** The dev-shell env is read live and passed in additively, so scrub
  anything you don't want the agent to see from the dev-shell before launching.
  Pass the agent's own credentials explicitly (e.g. `-e OPENAI_API_KEY`).
- **Change deps → re-`gen`.** Editing `flake.nix` and reloading direnv re-runs
  `gen` automatically; the closure stays in sync.
- **`sudo` strips the gate.** Use `sudo -E` if you must, or the device goes
  inert.
- **Nothing happening?** Walk the checklist in
  [../docs/usage.md#troubleshooting](../docs/usage.md#troubleshooting) — most
  often `DIRENV_DIR` is empty or `gen` hasn't run.

> `codex` here is illustrative; swap it for any agent CLI packaged in nixpkgs
> (or add your own to the dev-shell). The mechanism doesn't care which agent it
> is — it just makes the dev-shell's closure available in the container.
