#!/usr/bin/env bash
# Test: can a CDI-embedded createRuntime hook find the entrypoint and wrap it
# so PATH becomes ADDITIVE (devshell prefix prepended to the image's real PATH),
# entirely from `--device`, on a stock image, for any command?
set -uo pipefail

IMAGE=${IMAGE:-debian:bookworm-slim}
WORK=$(mktemp -d)
chmod 755 "$WORK"   # mktemp gives 0700; CDI resolver must traverse the spec/hook paths
trap 'rm -rf "$WORK"' EXIT
PASS=0 FAIL=0

ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
no()  { printf '  \033[31mFAIL\033[0m %s\n     expected to contain: %s\n     got: %s\n' "$1" "$2" "$3"; FAIL=$((FAIL+1)); }
has() { if printf '%s' "$3" | grep -qF -- "$2"; then ok "$1"; else no "$1" "$2" "$3"; fi; }
hasnt(){ if printf '%s' "$3" | grep -qF -- "$2"; then no "$1" "absence of: $2" "$3"; else ok "$1"; fi; }

# ── 1. a fake "devshell prefix" with a tool that exists ONLY here ──────────────
mkdir -p "$WORK/prefix"
printf '#!/bin/sh\necho PREFIXTOOL-RAN\necho "toolPATH=$PATH"\n' > "$WORK/prefix/prefixtool"
chmod +x "$WORK/prefix/prefixtool"

# ── 2. the createRuntime hook: find process.args[0], wrap it additively ────────
cat > "$WORK/devshell-hook" <<'HOOK'
#!/bin/sh
# Runs in the host namespace at createRuntime: can READ config.json AND WRITE the
# rootfs (mounted by now). Best-effort: any failure -> exit 0, leave container intact.
state=$(cat) || exit 0
bundle=$(printf '%s' "$state" | jq -r '.bundle // empty') || exit 0
cfg="$bundle/config.json"; [ -f "$cfg" ] || exit 0
rootfs=$(jq -r '.root.path' "$cfg"); case $rootfs in /*) ;; *) rootfs="$bundle/$rootfs";; esac

prefix=$(jq -r '(.process.env[]|select(startswith("DEVSHELL_PREFIX=")))|sub("^DEVSHELL_PREFIX=";"")' "$cfg" 2>/dev/null | head -1)
[ -n "$prefix" ] || exit 0
entry=$(jq -r '.process.args[0] // empty' "$cfg"); [ -n "$entry" ] || exit 0

imgpath=$(jq -r '(.process.env[]|select(startswith("PATH=")))|sub("^PATH=";"")' "$cfg" 2>/dev/null | head -1)

# write a PATH-prepending shim at container-path $1 that execs the real target $2
wrap_at() {
  wd=$(dirname "$rootfs$1"); [ -d "$wd" ] || mkdir -p "$wd" 2>/dev/null || return 1
  [ -w "$wd" ] || return 1
  printf '#!/bin/sh\nexport PATH="%s:$PATH"\nexec "%s" "$@"\n' "$prefix" "$2" > "$rootfs$1" || return 1
  chmod +x "$rootfs$1" 2>/dev/null
}

# container path -> HOST-accessible path. The hook runs in the host ns, where the
# container's bind-mounts aren't visible; map a path under a bind to its mount source,
# else fall back to the rootfs overlay (which IS visible here).
# NB: config.json is the OCI spec -> mounts use .destination/.source
# (CDI's hostPath/containerPath have already been translated by the runtime).
host_of() {
  # bind .destination as $d BEFORE the $cp pipe (else startswith reads $cp.destination)
  best=$(jq -c --arg cp "$1" '[.mounts[]? | (.destination // "") as $d | select($d!="" and ($cp|startswith($d)))] | sort_by(.destination|length) | last' "$cfg" 2>/dev/null)
  if [ -n "$best" ] && [ "$best" != null ]; then
    hcp=$(printf '%s' "$best"|jq -r .destination); hhp=$(printf '%s' "$best"|jq -r .source)
    printf '%s%s' "$hhp" "${1#"$hcp"}"
  else
    printf '%s%s' "$rootfs" "$1"
  fi
}

case $entry in
  /*) # absolute entrypoint: crun execs it directly -> wrap in place (move real aside)
      [ -e "$rootfs$entry" ] && [ -w "$(dirname "$rootfs$entry")" ] || exit 0
      [ -e "$rootfs$entry.real" ] || mv "$rootfs$entry" "$rootfs$entry.real" || exit 0
      wrap_at "$entry" "$entry.real" ;;
  *)  # relative: resolve `command -v`-style across prefix + image PATH (host-mapped).
      # Space-split (not IFS=:) so host_of's own command-subst isn't affected.
      real=""
      for d in $(printf '%s:%s' "$prefix" "$imgpath" | tr ':' ' '); do
        [ -n "$d" ] && [ -x "$(host_of "$d/$entry")" ] && { real="$d/$entry"; break; }
      done
      [ -n "$real" ] || exit 0
      # ...then shadow it with a shim in the FIRST image-PATH dir, so crun finds it first
      shimdir=$(printf '%s' "$imgpath" | cut -d: -f1)
      if [ "$shimdir/$entry" = "$real" ]; then            # shim would clobber the real -> move aside
        mv "$rootfs$real" "$rootfs$real.real" && real="$real.real" || exit 0
      fi
      wrap_at "$shimdir/$entry" "$real" ;;
esac
exit 0
HOOK
chmod +x "$WORK/devshell-hook"

# ── 3. the CDI spec: embed the hook, mount the prefix, inject DEVSHELL_PREFIX ──
mkdir -p "$WORK/cdi"
cat > "$WORK/cdi/devshell.json" <<EOF
{ "cdiVersion":"0.6.0","kind":"direnv.local/devshell",
  "devices":[{"name":"shell","containerEdits":{
    "env":["DEVSHELL_PREFIX=/devshellprefix"],
    "mounts":[{"hostPath":"$WORK/prefix","containerPath":"/devshellprefix","options":["ro","bind"]}],
    "hooks":[{"hookName":"createRuntime","path":"$WORK/devshell-hook"}]
  }}]}
EOF

run() { timeout 60 podman run --rm --network=none \
          --cdi-spec-dir "$WORK/cdi" --device direnv.local/devshell=shell "$@" 2>&1; }

echo "image: $IMAGE   (attach with ONLY --device; no --entrypoint, no PATH set in CDI)"
echo

echo "T1  entrypoint=bash: PATH is ADDITIVE (prefix prepended, base preserved)"
o=$(run "$IMAGE" bash -c 'echo "PATH=$PATH"')
has   "  prefix is first"        "PATH=/devshellprefix:" "$o"
has   "  image base preserved"   ":/usr/bin"             "$o"

echo "T2  a devshell-only tool is reachable via the additive PATH"
o=$(run "$IMAGE" bash -c 'prefixtool'); has "  prefixtool found+ran" "PREFIXTOOL-RAN" "$o"

echo "T3  base-image tools STILL work (not overridden)"
o=$(run "$IMAGE" bash -c 'cat /etc/hostname >/dev/null && echo BASE-CAT-OK'); has "  cat still resolves" "BASE-CAT-OK" "$o"

echo "T4  the wrapped entrypoint actually execs the REAL binary"
o=$(run "$IMAGE" bash -c 'echo HELLO-FROM-REAL-BASH'); has "  real bash ran" "HELLO-FROM-REAL-BASH" "$o"

echo "T5  works for a DIFFERENT entrypoint (sh) — exercises the recursion guard"
o=$(run --entrypoint sh "$IMAGE" -c 'echo "PATH=$PATH"'); has "  sh wrapped, additive, no hang" "PATH=/devshellprefix:" "$o"

echo "T6  control: WITHOUT the device, PATH is the plain image default"
o=$(timeout 60 podman run --rm --network=none "$IMAGE" bash -c 'echo "PATH=$PATH"' 2>&1)
hasnt "  no devshell prefix leaks in" "/devshellprefix" "$o"

echo "T7  devshell-only tool as the *bare* entrypoint (resolved via prefix + shadow shim)"
o=$(run "$IMAGE" prefixtool); has "  bare devshell-only entrypoint runs" "PREFIXTOOL-RAN" "$o"

echo "T8  additive PATH holds even when entry is a devshell-only tool"
o=$(run "$IMAGE" sh -c 'prefixtool && echo "PATH=$PATH"')
has "  prefix present" "/devshellprefix" "$o"; has "  base present too" ":/usr/bin" "$o"

echo "T9  LIMITATION: ABSOLUTE path into the RO prefix mount — runs, but NOT additive"
o=$(run "$IMAGE" /devshellprefix/prefixtool)
has   "  it still runs (absolute path is mounted)"          "PREFIXTOOL-RAN" "$o"
hasnt "  but PATH was NOT made additive (no wrap possible)" "toolPATH=/devshellprefix" "$o"

echo "T10 ABSOLUTE path into the IMAGE rootfs (writable) — DOES get wrapped"
o=$(run --entrypoint /bin/bash "$IMAGE" -c 'echo "PATH=$PATH"')
has "  abs image-path entrypoint is additive" "PATH=/devshellprefix:" "$o"

echo
printf 'RESULTS: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
