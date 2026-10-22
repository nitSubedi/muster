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
		st, _ := os.Stat(filepath.Join(dir, "node.pem"))
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

func TestConcurrentFirstRunAgree(t *testing.T) {
	dir := t.TempDir()
	fps := make(chan string, 8)
	for i := 0; i < 8; i++ {
		go func() {
			id, err := LoadOrCreate(dir)
			if err != nil {
				t.Error(err)
			}
			fps <- id.FP
		}()
	}
	first := <-fps
	for i := 1; i < 8; i++ {
		if fp := <-fps; fp != first {
			t.Fatalf("concurrent first runs produced different identities")
		}
	}
}
