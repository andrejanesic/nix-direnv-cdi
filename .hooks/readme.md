# Git hooks

Tracked, opt-in git hooks for this repo. Git does not run hooks from a tracked
directory by default, so activate them once per clone:

```sh
bash ./.hooks/install.sh
```

This sets `core.hooksPath` to `.hooks`. To disable:

```sh
git config --unset core.hooksPath
```

## Hooks

| Hook | Does |
|------|------|
| `pre-commit` | Runs `gofmt -l` on staged `.go` files; blocks the commit if any need formatting. Mirrors the CI **Format check** step. |

To bypass a hook for one commit (use sparingly): `git commit --no-verify`.
