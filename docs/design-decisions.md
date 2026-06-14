# Design decisions

Why the tool is shaped the way it is. Each decision records the alternative(s)
rejected and why. The empirical findings behind these choices are distilled in
[internals.md](internals.md) and [limitations.md](limitations.md); the original
design exploration is preserved in the git history.

## D1 — One generic device, not one per project

**Decision:** a single device `nix-direnv-cdi.org/env=current` for all projects.

The original design baked each project's closure + env into its own CDI spec,
named by a fingerprint of the project root. That meant **N projects → N
registered devices**, which we explicitly did not want. Since the closure can be
injected dynamically at run time (D2), nothing project-specific needs to live in
the device — so there is exactly one, and the fingerprint disappears.

## D2 — Inject mounts dynamically from the hook, not statically in the spec

**Decision:** the `createRuntime` hook bind-mounts the closure at run time.

CDI resolves `--device kind=name` purely by name, *before* the hook runs, and
applies the spec's static mounts then. So per-project mounts in the spec ⇒
per-project device names ⇒ a fingerprint. Injecting the mounts from the hook
instead keeps the device generic while still mounting only the **surgical**
closure (not the whole store — see D3).

## D3 — `createRuntime` + namespace entry, over the alternatives

We surveyed every way to get the closure into the container:

| Approach | Verdict |
|----------|---------|
| **createRuntime hook enters the container mount ns** | ✅ chosen — OCI-standard (crun + runc), works rootless (verified) |
| Static CDI `mounts` per project | ✗ forces per-project devices (the thing we're removing) |
| Mount the **entire `/nix/store`** read-only (one generic mount) | ✗ rejected: too broad an exposure |
| Host-side `mount` from the hook (no ns entry) | ✗ impossible: doesn't propagate into the container's mount ns |
| `createContainer` hook | ✗ runs in the container ns but is rootless-blocked (needs `CAP_SYS_ADMIN`) |
| podman `precreate` hook | ✗ works but is **podman-only** (not OCI; docker can't) |
| Mount-propagation trick (`rshared` parent) | ✗ fragile; defeated by `rprivate` defaults + rootless isolation |
| A custom runtime shim (NVIDIA JIT-CDI style) | ✗ heavier; we stay on standard CDI hooks |

The chosen approach has solid prior art: entering the container's mount namespace
is exactly how NVIDIA's `libnvidia-container` injects its mounts (mechanics in
[mechanisms.md](mechanisms.md#2-dynamic-mount-injection-the-closure)).

## D4 — Gate on being in the loaded dev-shell

**Decision:** the hook acts only when `DIRENV_DIR` is present in its inherited
environment; otherwise it no-ops.

Being in the loaded dev-shell means you ran `direnv allow` and entered it — that
*is* the authorization. We deliberately do not expose a dev-shell you haven't
entered (and approved). See [security.md](security.md).

## D5 — Read `PATH`/env from `DIRENV_DIFF` at run time, not baked

**Decision:** the hook decodes the additive-`PATH` prefix and dev-shell env vars
from the inherited `DIRENV_DIFF` when the container is created.

Benefits: the env is always **fresh** (reflects the shell now), the dev-shell
env (which may contain secrets) is **never written to a spec file on disk**, and
it dovetails with the gate (no loaded shell → no `DIRENV_DIFF` → nothing wired).

## D6 — Closure from `gen` → `mounts.json`, not computed in the hook

**Decision:** `gen` pre-computes the closure (gcroot → `nix-store -qR`) into
`.direnv/cdi/mounts.json`; the hook just reads it.

Running `nix-store` inside the hook would be slow per-run and require nix on the
hook's (sanitized) `PATH`. Pre-computing is fast and hermetic. Crucially, the
closure derives from the **gcroot**, not `DIRENV_DIFF`, so `gen` can run *inside*
`.envrc` after `use flake` — which is what makes the `use cdi` integration
possible.

## Consequence

D1–D6 compose into: **one generic device + a hook that reads the live dev-shell
and a pre-computed closure**. The device says "current environment"; the
launching shell says which project, at run time. See
[mechanisms.md](mechanisms.md) for how that plays out end to end.
