package fingerprint

import (
	"regexp"
	"testing"
)

// hexName matches lowercase hexadecimal, a strict subset of the CDI device-name
// grammar (CDI SPEC: a leading/trailing alphanumeric around alphanumerics and
// the set _-.:+). Milestone 2 will additionally validate generated device names
// against the real CNCF parser once cdispec is wired into app code.
var hexName = regexp.MustCompile(`^[0-9a-f]+$`)

func TestComputeDeterministic(t *testing.T) {
	const root = "/home/user/project"
	if a, b := Compute(root), Compute(root); a != b {
		t.Fatalf("not deterministic: %q != %q", a, b)
	}
	if got := len(Compute(root)); got != Length {
		t.Fatalf("length = %d, want %d", got, Length)
	}
}

func TestComputeStableAcrossUncleanPaths(t *testing.T) {
	if Compute("/home/user/project") != Compute("/home/user/./project/") {
		t.Fatal("unclean path variants should fingerprint identically")
	}
}

func TestComputeDistinctRoots(t *testing.T) {
	if Compute("/a/one") == Compute("/a/two") {
		t.Fatal("distinct roots must yield distinct fingerprints")
	}
}

func TestComputeValidCDIDeviceName(t *testing.T) {
	got := Compute("/home/user/project")
	if !hexName.MatchString(got) {
		t.Fatalf("fingerprint %q is not lowercase hex; invalid as a CDI device name", got)
	}
}
