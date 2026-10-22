package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadOrCreateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "muster", "config.json")

	first, created, err := LoadOrCreate(path)
	if err != nil || !created {
		t.Fatalf("first load: created=%v err=%v", created, err)
	}
	if len(first.Token) < 40 || first.NodeID == "" {
		t.Fatalf("weak or missing values: %+v", first)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
		}
	}

	second, created, err := LoadOrCreate(path)
	if err != nil || created {
		t.Fatalf("second load: created=%v err=%v", created, err)
	}
	if second != first {
		t.Fatalf("config changed between loads: %+v vs %+v", first, second)
	}
}

func TestLoadRejectsIncompleteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"node_id":"x"}`), 0o600)
	if _, _, err := LoadOrCreate(path); err == nil {
		t.Fatal("expected error for missing token")
	}
}

func TestClusterID(t *testing.T) {
	a, b := ClusterID("token-a"), ClusterID("token-b")
	if a == b || len(a) != 16 {
		t.Fatalf("a=%q b=%q", a, b)
	}
	if ClusterID("token-a") != a {
		t.Fatal("not deterministic")
	}
}

func TestTokenEqual(t *testing.T) {
	if !TokenEqual("abc", "abc") || TokenEqual("abc", "abd") || TokenEqual("abc", "") {
		t.Fatal("TokenEqual wrong")
	}
}
