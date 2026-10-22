package tlsid

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadOrCreateIsStable(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.FP != b.FP || len(a.FP) != 64 {
		t.Fatalf("fingerprints %q %q", a.FP, b.FP)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, "node.key"))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("key mode %v", st.Mode().Perm())
		}
	}
	other, _ := LoadOrCreate(t.TempDir())
	if other.FP == a.FP {
		t.Fatal("two nodes share a fingerprint")
	}
}

func TestPeerFPPlain(t *testing.T) {
	if PeerFP(nil) != "" {
		t.Fatal("plain connection has a fingerprint")
	}
}
