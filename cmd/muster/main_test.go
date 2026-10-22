package main

import (
	"strings"
	"testing"
)

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

func TestDiagnose(t *testing.T) {
	cases := map[string]string{
		`Get "http://192.168.1.98:7878/muster/v1/node": dial tcp 192.168.1.98:7878: connect: connection refused`: "firewall",
		`dial tcp 10.0.0.2:7878: connect: no route to host`:                                                      "firewall",
		`Get "http://x/muster/v1/node": dial tcp x: i/o timeout`:                                                 "asleep",
		`peer failed authentication (different cluster token?)`:                                                  "muster join",
		`401 Unauthorized`: "muster join",
	}
	for in, want := range cases {
		if got := diagnose(in); !strings.Contains(got, want) {
			t.Errorf("diagnose(%q) = %q, want mention of %q", in, got, want)
		}
	}
}
