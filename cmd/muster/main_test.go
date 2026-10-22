package main

import "testing"

func TestNormalizeBackend(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:11434":  "http://127.0.0.1:11434",
		"http://127.0.0.1:11434/": "http://127.0.0.1:11434",
		"0.0.0.0:11434":           "http://127.0.0.1:11434",
		"0.0.0.0":                 "http://127.0.0.1:11434",
		"localhost":               "http://localhost:11434",
		"https://gpu.lan:8443":    "https://gpu.lan:8443",
	}
	for in, want := range cases {
		if got := normalizeBackend(in); got != want {
			t.Errorf("normalizeBackend(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListenPort(t *testing.T) {
	if p, err := listenPort(":7878"); err != nil || p != 7878 {
		t.Fatalf("got %d %v", p, err)
	}
	if _, err := listenPort("7878"); err == nil {
		t.Fatal("expected error for missing colon")
	}
}
