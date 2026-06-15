package devshell

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// encodeDirenvDiff produces a DIRENV_DIFF value the way direnv (gzenv) does:
// PADDED URL-safe base64 of zlib-compressed JSON {"p":prev,"n":next}. Used to
// drive discover with a faked environment.
func encodeDirenvDiff(t *testing.T, prev, next map[string]string) string {
	t.Helper()
	payload, err := json.Marshal(struct {
		Prev map[string]string `json:"p"`
		Next map[string]string `json:"n"`
	}{prev, next})
	if err != nil {
		t.Fatalf("marshal diff: %v", err)
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return base64.URLEncoding.EncodeToString(buf.Bytes())
}

// fakeEnv builds a getenvFunc over a map.
func fakeEnv(m map[string]string) getenvFunc {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestDiscover_FakeEnvAndClosure(t *testing.T) {
	const projectRoot = "/home/u/proj"

	prevPath := "/usr/bin:/bin"
	newPath := "/nix/store/aaa-go/bin:/nix/store/bbb-coreutils/bin:" + projectRoot + "/.direnv/bin:/usr/bin:/bin"

	diff := encodeDirenvDiff(t,
		map[string]string{"PATH": prevPath},
		map[string]string{
			"PATH":         newPath,
			"IN_NIX_SHELL": "impure",
			"CC":           "gcc",
			"NIX_STORE":    "/nix/store",
			// direnv bookkeeping that must be excluded:
			"DIRENV_DIR":  "-" + projectRoot,
			"DIRENV_FILE": projectRoot + "/.envrc",
			"DIRENV_DIFF": "ignored",
		},
	)

	env := map[string]string{
		"DIRENV_DIR":  "-" + projectRoot, // leading '-' must be stripped
		"DIRENV_DIFF": diff,
	}

	fakeClosure := []string{
		"/nix/store/aaa-go",
		"/nix/store/bbb-coreutils",
		"/nix/store/ccc-glibc",
	}
	gcrootResolver := func(root string, _ getenvFunc) (string, error) {
		if root != projectRoot {
			t.Fatalf("gcroot resolver got root %q, want %q", root, projectRoot)
		}
		return "/nix/store/xyz-nix-shell-env", nil
	}
	listClosure := func(gcroot string) ([]string, error) {
		if gcroot != "/nix/store/xyz-nix-shell-env" {
			t.Fatalf("closure lister got gcroot %q", gcroot)
		}
		return fakeClosure, nil
	}

	ds, err := discover(fakeEnv(env), nil, gcrootResolver, listClosure)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	// ProjectRoot strips the single leading '-'.
	if ds.ProjectRoot != projectRoot {
		t.Errorf("ProjectRoot = %q, want %q", ds.ProjectRoot, projectRoot)
	}

	// Prefix: the dev-shell-added PATH dirs, in order, excluding pre-existing.
	wantPrefix := []string{
		"/nix/store/aaa-go/bin",
		"/nix/store/bbb-coreutils/bin",
		projectRoot + "/.direnv/bin",
	}
	if !reflect.DeepEqual(ds.Prefix, wantPrefix) {
		t.Errorf("Prefix = %v, want %v", ds.Prefix, wantPrefix)
	}

	// Env excludes PATH and all DIRENV_*.
	if _, ok := ds.Env["PATH"]; ok {
		t.Errorf("Env must not contain PATH, got %q", ds.Env["PATH"])
	}
	for k := range ds.Env {
		if len(k) >= 7 && k[:7] == "DIRENV_" {
			t.Errorf("Env must not contain DIRENV_* key, got %q", k)
		}
	}
	// Env captures the dev-shell vars.
	for k, want := range map[string]string{"IN_NIX_SHELL": "impure", "CC": "gcc", "NIX_STORE": "/nix/store"} {
		if ds.Env[k] != want {
			t.Errorf("Env[%q] = %q, want %q", k, ds.Env[k], want)
		}
	}

	// Closure populated from the fake.
	if !reflect.DeepEqual(ds.Closure, fakeClosure) {
		t.Errorf("Closure = %v, want %v", ds.Closure, fakeClosure)
	}
}

func TestReadMounts_AcceptsStorePaths(t *testing.T) {
	want := []string{"/nix/store/aaa-go", "/nix/store/bbb-coreutils/bin"}
	path := filepath.Join(t.TempDir(), "mounts.json")
	if err := WriteMounts(path, want); err != nil {
		t.Fatalf("WriteMounts: %v", err)
	}
	got, err := ReadMounts(path, DefaultStoreDir)
	if err != nil {
		t.Fatalf("ReadMounts: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadMounts = %v, want %v", got, want)
	}
}

func TestReadMounts_RelocatedStoreDir(t *testing.T) {
	// A relocated store (NIX_STORE_DIR) is honoured: paths under it are accepted,
	// and the default /nix/store is then rejected.
	want := []string{"/custom/store/aaa-go"}
	path := filepath.Join(t.TempDir(), "mounts.json")
	if err := WriteMounts(path, append([]string{"/nix/store/bbb"}, want...)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMounts(path, "/custom/store"); err == nil {
		t.Fatal("expected /nix/store entry to be rejected under relocated store")
	}
	if err := WriteMounts(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMounts(path, "/custom/store")
	if err != nil {
		t.Fatalf("ReadMounts (relocated): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadMounts = %v, want %v", got, want)
	}
}

func TestReadMounts_RejectsUnsafePaths(t *testing.T) {
	// Each fixture has one tampered entry alongside a legitimate one; the whole
	// file must be rejected (fail-closed) so nothing gets bind-mounted.
	cases := map[string]string{
		"traversal escaping the store": "/nix/store/../../etc",
		"absolute non-store path":      "/etc/passwd",
		"relative path":                "nix/store/aaa",
		"dot-dot segment":              "/nix/store/aaa/../../../root/.ssh",
		"the store root itself":        "/nix/store",
		"trailing slash":               "/nix/store/aaa/",
		"empty string":                 "",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mounts.json")
			data, err := json.Marshal(MountsFile{Closure: []string{"/nix/store/aaa-go", bad}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadMounts(path, DefaultStoreDir); err == nil {
				t.Fatalf("ReadMounts accepted unsafe path %q; want error", bad)
			}
		})
	}
}

func TestProjectRoot_FallbackToGetwd(t *testing.T) {
	// DIRENV_DIR unset -> fall back to getwd.
	root, err := projectRoot(fakeEnv(map[string]string{}), func() (string, error) {
		return "/cwd/here", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if root != "/cwd/here" {
		t.Errorf("got %q, want /cwd/here", root)
	}
}

func TestProjectRoot_StripsSingleLeadingDash(t *testing.T) {
	root, err := projectRoot(fakeEnv(map[string]string{"DIRENV_DIR": "-/a/b"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if root != "/a/b" {
		t.Errorf("got %q, want /a/b", root)
	}
}

func TestDecodeDirenvDiff_RoundTrip(t *testing.T) {
	prev := map[string]string{"PATH": "/old"}
	next := map[string]string{"PATH": "/new:/old", "FOO": "bar"}
	enc := encodeDirenvDiff(t, prev, next)
	gotPrev, gotNext, err := decodeDirenvDiff(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(gotPrev, prev) || !reflect.DeepEqual(gotNext, next) {
		t.Errorf("round-trip mismatch: prev=%v next=%v", gotPrev, gotNext)
	}
}

func TestDecodeDirenvDiff_AcceptsPadding(t *testing.T) {
	// direnv (gzenv) emits PADDED URL-safe base64; decoding must accept the '='
	// padding that appears whenever the compressed length isn't a multiple of 3.
	// Regression: a raw-URL decode rejected the padding ("illegal base64 data"),
	// breaking `gen` whenever the loaded env happened to encode to a padded length.
	sawPadding := false
	for i := 0; i < 8; i++ {
		pad := strings.Repeat("x", i)
		enc := encodeDirenvDiff(t, nil, map[string]string{"PATH": "/p", "PAD": pad})
		if strings.HasSuffix(enc, "=") {
			sawPadding = true
		}
		_, gotNext, err := decodeDirenvDiff(enc)
		if err != nil {
			t.Fatalf("decode failed (i=%d, padded=%v): %v", i, strings.HasSuffix(enc, "="), err)
		}
		if gotNext["PAD"] != pad {
			t.Errorf("round-trip mismatch at i=%d: PAD=%q", i, gotNext["PAD"])
		}
	}
	if !sawPadding {
		t.Fatal("no fixture exercised base64 padding; test would be meaningless")
	}
}

func TestDecodeDirenvDiff_RejectsBomb(t *testing.T) {
	// A small compressed payload that inflates past the cap must be refused
	// rather than read into memory unbounded.
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(bytes.Repeat([]byte{'A'}, 32<<20)); err != nil { // 32 MiB of 'A'
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString(buf.Bytes())
	if _, _, err := decodeDirenvDiff(enc); err == nil {
		t.Fatal("decodeDirenvDiff accepted a decompression bomb; want error")
	}
}

func TestPrefixFromDiff_DedupAndOrder(t *testing.T) {
	got := prefixFromDiff("/b:/c", "/a:/b:/a:/d:/c")
	want := []string{"/a", "/d"} // /b,/c pre-existing; /a deduped; order preserved
	if !reflect.DeepEqual(got, want) {
		t.Errorf("prefixFromDiff = %v, want %v", got, want)
	}
}

func TestDiscover_NoDirenvDiff(t *testing.T) {
	_, err := discover(
		fakeEnv(map[string]string{"DIRENV_DIR": "-/x"}),
		nil,
		func(string, getenvFunc) (string, error) { return "", nil },
		func(string) ([]string, error) { return nil, nil },
	)
	if err == nil {
		t.Fatal("expected error when DIRENV_DIFF is unset")
	}
}

func TestEnvFromDiff_SortedKeysStable(t *testing.T) {
	// Sanity: envFromDiff returns a map; keys retrievable deterministically.
	env := envFromDiff(map[string]string{"Z": "1", "A": "2", "PATH": "x", "DIRENV_DIR": "y"})
	var keys []string
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"A", "Z"}) {
		t.Errorf("keys = %v, want [A Z]", keys)
	}
}
