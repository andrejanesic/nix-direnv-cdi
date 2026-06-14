# Mechanisms

How the dev-shell actually reaches inside the container. Three mechanisms stack:
the **CDI device + hook**, **dynamic mount injection** (the closure), and
**additive `PATH`** (the entrypoint wrapper). The low-level tricks each relies on
are in [gotchas.md](gotchas.md).

## 1. The CDI device and the createRuntime hook

A **CDI** (Container Device Interface) spec is a static JSON file describing a
"device". When you `podman run --device nix-direnv.cdi/shell=devshell …`, the
runtime frontend injects that device's `containerEdits` into the OCI
`config.json`. Our device's only edit is a single hook:

```json
{ "hookName": "createRuntime", "path": "<installed binary>", "args": ["nix-direnv-cdi","hook"] }
```

Because it's a **standard OCI hook embedded in the CDI device**, it runs under
both **crun** (podman) and **runc** (docker) — the device is cross-runtime, and
opt-in (it only runs on containers you attach it to).

`createRuntime` is the chosen lifecycle stage because it is the one that runs **in
the host (runtime) namespace, after the container's mount namespace and mounts
exist, but before `pivot_root`** — so it can read `config.json` and the OCI
`State` (which carries the container `pid`) *and* affect the container's rootfs.

## 2. Dynamic mount injection (the closure)

The dev-shell's tools live in `/nix/store`. The container must see those store
paths. We do **not** list them in the CDI spec (that would make the device
project-specific); instead the hook bind-mounts them at run time.

**Why not just mount host-side?** The hook runs in the host mount namespace, but
the container has its own mount namespace with private/slave propagation — a
bind mount the hook makes host-side under the rootfs does **not** appear in the
container. (File *writes* into the rootfs do propagate; mounts do not.)

**What works:** the hook **enters the container's mount namespace** (via the
`pid` from the OCI State) and bind-mounts there:

1. read `<project>/.direnv/cdi/mounts.json` (the closure, located via the
   inherited `DIRENV_DIR`);
2. `unshare(CLONE_FS)` then `setns(CLONE_NEWNS)` into the container's mount ns
   (on a dedicated, discarded OS thread — see [gotchas.md](gotchas.md));
3. for each closure path, bind it onto `<rootfs>/<path>` (read-only,
   best-effort).

Source paths (`/nix/store/X`) are reachable from inside the container's mount ns
*before* `pivot_root`, and mounts created under `root.path` survive `pivot_root`,
appearing at `/nix/store/X`. This is the same technique NVIDIA's
`libnvidia-container` uses to inject GPU mounts.

## 3. Additive `PATH` (the entrypoint wrapper)

The dev-shell needs to be *prepended* to the image's `PATH`, not replace it. But:

- CDI `env` is **set-only** — it can write `PATH=…` but can't append to the
  image's real `PATH`.
- OCI hooks **cannot modify `process.env`/`process.args`**.

So additive `PATH` can't be expressed as data. The hook instead **wraps the
entrypoint**: it writes a shim that prepends the prefix and exports the dev-shell
env, then execs the real entrypoint.

```sh
#!/bin/sh
export PATH="<nix prefix>:$PATH"
export CC='gcc'        # dev-shell env vars, single-quoted
exec "<real entrypoint>" "$@"
```

The prefix and env come from decoding the inherited **`DIRENV_DIFF`** at run
time (not baked into the spec). Resolution mirrors `command -v`:

- **relative** entrypoint → resolve across `prefix` (host-accessible `/nix`
  paths) then the image `PATH`; drop the shim into the first image-`PATH` dir so
  the runtime finds it first.
- **absolute** entrypoint in the writable rootfs → wrap in place (move the real
  aside).
- **absolute path into the read-only store** → left intact (the **T9**
  limitation; see [caveats.md](caveats.md)).

### Behaviour matrix (T1–T10)

The additive-`PATH` contract is pinned by a numbered matrix; the test suite
asserts against these IDs (grep `T1`…`T10` in `*_test.go`).

| ID | Behaviour |
|----|-----------|
| T1 | entrypoint = shell → `PATH` additive (prefix prepended, image base preserved) |
| T2 | a dev-shell-only tool is reachable via the additive `PATH` |
| T3 | base-image tools still resolve |
| T4 | the wrapped entrypoint execs the real binary |
| T5 | works for `sh` — no shebang recursion |
| T6 | control: **without** the device, `PATH` is the plain image default |
| T7 | a dev-shell-only tool as the **bare** entrypoint runs |
| T8 | additive `PATH` holds even when the entry is a dev-shell-only tool |
| T9 | limitation: absolute path into a read-only mount runs but is **not** additive |
| T10 | absolute path into the **writable** rootfs **is** wrapped |

## Putting it together (per `podman run`)

1. Runtime injects the device → the hook is registered in `config.json`.
2. Runtime performs the container's mounts, then fires the `createRuntime` hook.
3. Hook gates on `DIRENV_DIR`; absent → no-op (inert).
4. Hook injects the closure (mechanism 2) and wraps the entrypoint (mechanism 3).
5. `pivot_root`; the (possibly wrapped) entrypoint execs with the dev-shell
   mounted and on `PATH`.

See [data-flow.md](data-flow.md) for the full timeline and [security.md](security.md)
for the gate's role as the authorization model.
