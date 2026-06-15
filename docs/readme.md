# nix-direnv-cdi — documentation

Reference docs for nix-direnv-cdi. For the shortest path from zero to a running
container, start with [usage.md](usage.md); for a worked end-to-end example
(a coding agent in a container), see [../example/](../example/readme.md).

## Contents

| Doc | Audience | What it covers |
|-----|----------|----------------|
| [usage.md](usage.md) | users | The user guide: install, project setup, run, remove, and troubleshooting — all in one place. |
| [../example/](../example/readme.md) | users | A copy-pasteable project running a coding agent in a container via the device. |
| [architecture.md](architecture.md) | everyone | The big picture: the generic-device model, components, subcommands, and artifacts. |
| [mechanisms.md](mechanisms.md) | everyone | How it works end to end: the CDI hook, dynamic mount injection (ns-entry), additive `PATH`, and the full setup→generate→inject timeline. |
| [decisions.md](decisions.md) | everyone | Why it's shaped this way, with the alternatives considered and rejected. |
| [security.md](security.md) | users | The authorization model (the gate), exposure surface, read-only mounts, and secrets handling. |
| [limitations.md](limitations.md) | users | Limitations, non-goals, the runtime support matrix, and troubleshooting. |
| [release.md](release.md) | users + maintainers | Release channels, artifact verification, upgrade, rollback, and the release checklist. |
| [../CONTRIBUTING.md](../CONTRIBUTING.md) | contributors + maintainers | Build/test commands, validation policy, review checklists, hook debugging, and maintainer responsibilities. |
| [internals.md](internals.md) | maintainers | The non-obvious, load-bearing kernel/Go traps the implementation depends on. |

## Suggested reading order

Just want to use it? Read **[usage.md](usage.md)** and skim
**[../example/](../example/readme.md)**; the rest is background.

1. **[architecture.md](architecture.md)** — start here for the shape of the system.
2. **[mechanisms.md](mechanisms.md)** — how it actually runs, end to end.
3. **[decisions.md](decisions.md)** — why, and what was rejected.
4. **[security.md](security.md)** + **[limitations.md](limitations.md)** — the
   model and its edges (user-facing).
5. **[../CONTRIBUTING.md](../CONTRIBUTING.md)** — read before changing code;
   it has the validation policy and review checklists.
6. **[internals.md](internals.md)** — read before changing the hook or
   `nsmount`.
