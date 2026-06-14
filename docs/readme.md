# nix-direnv-cdi — documentation

Reference docs for nix-direnv-cdi. For a quick start and usage examples, see the
top-level [README](../README.md); for the full design history and milestones,
see [PLAN.md](../PLAN.md).

## Contents

| Doc | What it covers |
|-----|----------------|
| [architecture.md](architecture.md) | The big picture: the generic-device model, components, subcommands, and artifacts. |
| [mechanisms.md](mechanisms.md) | How it works at run time: the CDI hook, dynamic mount injection (ns-entry), and additive `PATH` wrapping. |
| [data-flow.md](data-flow.md) | End-to-end timeline (setup → generate → inject) and what data lives where. |
| [design-decisions.md](design-decisions.md) | Why it's shaped this way, with the alternatives considered and rejected. |
| [security.md](security.md) | The authorization model (the gate), exposure surface, read-only mounts, and secrets handling. |
| [gotchas.md](gotchas.md) | The non-obvious, load-bearing tricks and kernel/Go traps the implementation depends on. |
| [caveats.md](caveats.md) | Limitations, non-goals, the runtime support matrix, and deferred verification. |

## Suggested reading order

1. **[architecture.md](architecture.md)** — start here for the shape of the system.
2. **[mechanisms.md](mechanisms.md)** + **[data-flow.md](data-flow.md)** — how it actually runs.
3. **[design-decisions.md](design-decisions.md)** — why, and what was rejected.
4. **[security.md](security.md)** + **[caveats.md](caveats.md)** — the model and its edges.
5. **[gotchas.md](gotchas.md)** — read before changing the hook or `nsmount`.
