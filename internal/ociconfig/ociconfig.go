// Package ociconfig provides thin helpers over opencontainers/runtime-spec for
// reading the OCI container State (delivered to a hook on stdin) and the
// bundle's config.json. Field names follow the OCI spec: mounts use
// .destination/.source, the command is .process.args, env is .process.env, and
// the rootfs is .root.path.
package ociconfig

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	oci "github.com/opencontainers/runtime-spec/specs-go"
)

// State is the OCI container State a hook receives on stdin, extended with the
// non-standard `root` field. The OCI runtime-spec State carries only
// pid/bundle/id/…; podman additionally includes `root`, the absolute path to the
// container's already-mounted rootfs. We capture it because the standard
// `bundle` is not always usable: rootless podman reports bundle="/" (the real
// config.json lives elsewhere in its storage), and `root` is then the only
// reliable rootfs path.
type State struct {
	oci.State
	Root string `json:"root"`
}

// ReadState decodes the OCI container State a hook receives on stdin. The State
// carries the container pid plus the bundle/root paths used to locate config.json
// and the rootfs.
func ReadState(in io.Reader) (*State, error) {
	var st State
	if err := json.NewDecoder(in).Decode(&st); err != nil {
		return nil, fmt.Errorf("decode OCI state: %w", err)
	}
	return &st, nil
}

// LoadSpec reads and parses the container's config.json into an OCI runtime Spec,
// trying each candidate path (see configCandidates) until one is readable.
func LoadSpec(st *State) (*oci.Spec, error) {
	cands := configCandidates(st)
	var lastErr error
	for _, path := range cands {
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}
		var spec oci.Spec
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		return &spec, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no config.json candidate path")
	}
	return nil, fmt.Errorf("load config.json (tried %v): %w", cands, lastErr)
}

// configCandidates lists config.json paths to try, most-standard first:
//
//  1. <bundle>/config.json — the OCI-spec location. Works for Docker, rootful
//     podman, and any spec-compliant runtime that sets a real bundle path.
//  2. The podman rootless layout. Rootless podman reports bundle="/" in the hook
//     State, stashing the real config under
//     <graphroot>/overlay-containers/<id>/userdata/config.json. We derive
//     <graphroot> from the State's `root` (…/<graphroot>/overlay/<layer>/merged).
//     This is a best-effort fallback for the overlay storage driver; if it does
//     not match, LoadSpec simply reports the original failure.
func configCandidates(st *State) []string {
	var out []string
	if st.Bundle != "" {
		out = append(out, filepath.Join(st.Bundle, "config.json"))
	}
	// Derive podman's rootless bundle from root = <graphroot>/overlay/<layer>/merged.
	if st.Root != "" && st.ID != "" && strings.HasSuffix(st.Root, "/merged") {
		graphroot := filepath.Dir(filepath.Dir(filepath.Dir(st.Root)))
		if graphroot != "" && graphroot != "/" && graphroot != "." {
			out = append(out, filepath.Join(graphroot, "overlay-containers", st.ID, "userdata", "config.json"))
		}
	}
	return out
}
