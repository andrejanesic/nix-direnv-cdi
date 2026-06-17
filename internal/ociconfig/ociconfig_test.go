package ociconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	oci "github.com/opencontainers/runtime-spec/specs-go"
)

// writeConfig plants a minimal config.json at <dir>/config.json.
func writeConfig(t *testing.T, dir, json string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ociState builds the embedded runtime-spec State with id + bundle set.
func ociState(id, bundle string) oci.State {
	return oci.State{ID: id, Bundle: bundle}
}

const miniConfig = `{"ociVersion":"1.0.0","root":{"path":"merged"},` +
	`"process":{"args":["sh"],"env":["PATH=/usr/bin:/bin"]}}`

// TestReadState_CapturesRoot: podman's OCI State includes a non-standard `root`
// (the absolute rootfs path). The plain runtime-spec struct drops it; our State
// must keep it, because rootless podman's bundle is unusable.
func TestReadState_CapturesRoot(t *testing.T) {
	// Mirrors a real rootless-podman createRuntime State: bundle="/", root set.
	js := `{"ociVersion":"1.0","id":"abc123","pid":4242,` +
		`"root":"/home/u/.local/share/containers/storage/overlay/deadbeef/merged",` +
		`"bundle":"/","status":"created"}`
	st, err := ReadState(strings.NewReader(js))
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if st.Bundle != "/" {
		t.Errorf("Bundle = %q, want /", st.Bundle)
	}
	if st.Pid != 4242 || st.ID != "abc123" {
		t.Errorf("pid/id not decoded: %+v", st)
	}
	if !strings.HasSuffix(st.Root, "/overlay/deadbeef/merged") {
		t.Errorf("Root not captured: %q", st.Root)
	}
}

// TestLoadSpec_BundleConfig: the standard path — <bundle>/config.json is read
// (Docker, rootful podman).
func TestLoadSpec_BundleConfig(t *testing.T) {
	bundle := t.TempDir()
	writeConfig(t, bundle, miniConfig)
	st := &State{State: ociState("id1", bundle), Root: ""}
	spec, err := LoadSpec(st)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if len(spec.Process.Args) == 0 || spec.Process.Args[0] != "sh" {
		t.Errorf("spec.process.args = %v", spec.Process.Args)
	}
}

// TestLoadSpec_PodmanRootlessBundleSlash REPLICATES THE REAL BUG: rootless podman
// reports bundle="/", so <bundle>/config.json = /config.json is absent and the
// pre-fix Load() failed outright (making the whole device inert). The real
// config.json lives under <graphroot>/overlay-containers/<id>/userdata, derivable
// from the State's `root`. LoadSpec must find it.
func TestLoadSpec_PodmanRootlessBundleSlash(t *testing.T) {
	storage := t.TempDir() // stands in for <graphroot> = .../containers/storage
	id := "50e0a4835f25"

	// root = <graphroot>/overlay/<layer>/merged
	root := filepath.Join(storage, "overlay", "ef845453", "merged")
	// real config.json = <graphroot>/overlay-containers/<id>/userdata/config.json
	userdata := filepath.Join(storage, "overlay-containers", id, "userdata")
	writeConfig(t, userdata, miniConfig)

	st := &State{State: ociState(id, "/"), Root: root}
	spec, err := LoadSpec(st)
	if err != nil {
		t.Fatalf("LoadSpec did not recover the rootless config.json: %v", err)
	}
	if spec.Process.Args[0] != "sh" {
		t.Errorf("recovered spec wrong: args=%v", spec.Process.Args)
	}
}

// TestLoadSpec_Unreachable: neither the bundle nor the derived path exists. The
// error must name the candidates it tried (the hook then degrades to mount-only).
func TestLoadSpec_Unreachable(t *testing.T) {
	st := &State{State: ociState("id2", "/"), Root: "/nope/overlay/x/merged"}
	_, err := LoadSpec(st)
	if err == nil {
		t.Fatal("want error when no config.json is reachable")
	}
	if !strings.Contains(err.Error(), "overlay-containers") {
		t.Errorf("error should report the derived candidate, got: %v", err)
	}
}

// TestConfigCandidates_Order: bundle first, then the podman-derived path.
func TestConfigCandidates_Order(t *testing.T) {
	st := &State{
		State: ociState("cid", "/some/bundle"),
		Root:  "/var/lib/containers/storage/overlay/layer9/merged",
	}
	got := configCandidates(st)
	want := []string{
		"/some/bundle/config.json",
		"/var/lib/containers/storage/overlay-containers/cid/userdata/config.json",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("configCandidates = %v, want %v", got, want)
	}
}

// TestConfigCandidates_NonOverlayRootSkipped: a `root` that is not the overlay
// .../merged shape yields no derived candidate (only the bundle path).
func TestConfigCandidates_NonOverlayRootSkipped(t *testing.T) {
	st := &State{State: ociState("cid", "/b"), Root: "/var/lib/containers/storage/vfs/dir/layer"}
	got := configCandidates(st)
	if len(got) != 1 || got[0] != "/b/config.json" {
		t.Errorf("non-overlay root should not derive a candidate, got %v", got)
	}
}
