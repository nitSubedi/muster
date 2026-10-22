package node

import (
	"bufio"
	"strings"
	"testing"
)

func TestParseMeminfo(t *testing.T) {
	in := "MemFree:  100 kB\nMemTotal:       16314156 kB\n"
	if got := parseMeminfo(bufio.NewScanner(strings.NewReader(in))); got != 16314156*1024 {
		t.Fatalf("got %d", got)
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	got := parseNvidiaSMI("NVIDIA GeForce RTX 3090, 24576\nNVIDIA RTX A4000, 16376\ngarbage\n")
	if len(got) != 2 || got[0].Name != "NVIDIA GeForce RTX 3090" || got[0].MemoryBytes != 24576<<20 {
		t.Fatalf("got %+v", got)
	}
}

func TestHardwareReportsBasics(t *testing.T) {
	h := Hardware()
	if h.CPUs < 1 || h.OS == "" || h.Arch == "" {
		t.Fatalf("got %+v", h)
	}
}
