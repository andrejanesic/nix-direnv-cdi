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

	oci "github.com/opencontainers/runtime-spec/specs-go"
)

// ReadState decodes the OCI container State a hook receives on stdin. The State
// carries the bundle path from which config.json is loaded.
func ReadState(in io.Reader) (*oci.State, error) {
	var st oci.State
	if err := json.NewDecoder(in).Decode(&st); err != nil {
		return nil, fmt.Errorf("decode OCI state: %w", err)
	}
	return &st, nil
}

// Load reads and parses <bundleDir>/config.json into an OCI runtime Spec.
func Load(bundleDir string) (*oci.Spec, error) {
	path := filepath.Join(bundleDir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var spec oci.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &spec, nil
}
