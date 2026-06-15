#!/usr/bin/env bash
#
# Point git at the project's tracked hooks directory.
# Run once per clone: ./.hooks/install.sh

set +a -Eeuo pipefail

repo_root="$(git rev-parse --show-toplevel)"
git -C "$repo_root" config core.hooksPath .hooks

echo "core.hooksPath set to .hooks — project hooks are now active."
echo "To disable: git config --unset core.hooksPath"
