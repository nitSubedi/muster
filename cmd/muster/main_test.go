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
		"http://[::1]":            "http://[::1]:11434",
		"[::]:11434":              "http://[::1]:11434",
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

func TestHasFlag(t *testing.T) {
	if !hasFlag([]string{"--name", "x", "--ollama", "u"}, "ollama") || !hasFlag([]string{"-ollama=u"}, "ollama") {
		t.Fatal("flag not found")
	}
	if hasFlag([]string{"--ollama-ish"}, "ollama") || hasFlag([]string{"ollama"}, "name") {
		t.Fatal("false positive")
	}
}

func TestStablePathAvoidsHomebrewCellar(t *testing.T) {
	cases := [][3]string{
		{"/opt/homebrew/bin/muster", "/opt/homebrew/Cellar/muster/0.2.0/bin/muster", "/opt/homebrew/bin/muster"},
		{"/opt/homebrew/Cellar/muster/0.2.0/bin/muster", "/opt/homebrew/Cellar/muster/0.2.0/bin/muster", "/opt/homebrew/bin/muster"},
		{"/home/m/.local/bin/muster", "/home/m/.local/bin/muster", "/home/m/.local/bin/muster"},
	}
	for _, c := range cases {
		if got := stablePath(c[0], c[1]); got != c[2] {
			t.Errorf("stablePath(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestResolveBackendIgnoresMusterInEnv(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "http://localhost:7878")
	if got, err := resolveBackend("", 7878); err != nil || got != defaultOllama {
		t.Fatalf("got %q %v, want default", got, err)
	}
	t.Setenv("OLLAMA_HOST", "127.0.0.1:7878")
	if got, _ := resolveBackend("", 7878); got != defaultOllama {
		t.Fatalf("got %q", got)
	}
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:11500")
	if got, _ := resolveBackend("", 7878); got != "http://127.0.0.1:11500" {
		t.Fatalf("real Ollama in env ignored: %q", got)
	}
	t.Setenv("OLLAMA_HOST", "")
	if got, _ := resolveBackend("", 7878); got != defaultOllama {
		t.Fatalf("got %q", got)
	}
	if _, err := resolveBackend("localhost:7878", 7878); err == nil {
		t.Fatal("explicit --ollama pointing at Muster accepted")
	}
	if got, _ := resolveBackend("http://gpu.lan:7878", 7878); got != "http://gpu.lan:7878" {
		t.Fatalf("remote host on same port wrongly rejected: %q", got)
	}
}
