package node

import "testing"

func TestNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"llama3":                        "llama3:latest",
		"llama3:latest":                 "llama3:latest",
		"qwen2.5:7b":                    "qwen2.5:7b",
		"hf.co/org/model":               "hf.co/org/model:latest",
		"hf.co/org/model:Q4_K_M":        "hf.co/org/model:Q4_K_M",
		"registry.local:5000/team/m":    "registry.local:5000/team/m:latest",
		"registry.local:5000/team/m:v1": "registry.local:5000/team/m:v1",
	}
	for in, want := range cases {
		if got := NormalizeModel(in); got != want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccelMemoryPrefersGPU(t *testing.T) {
	i := Info{MemoryBytes: 64 << 30, GPUs: []GPU{{MemoryBytes: 24 << 30}}}
	if got := i.AccelMemory(); got != 24<<30 {
		t.Fatalf("got %d, want GPU memory", got)
	}
	i.GPUs = nil
	if got := i.AccelMemory(); got != 64<<30 {
		t.Fatalf("got %d, want system memory", got)
	}
}

func TestInfoModelMatchesNormalised(t *testing.T) {
	i := Info{Models: []Model{{Name: "llama3:latest", Loaded: true}}}
	m, ok := i.Model("llama3")
	if !ok || !m.Loaded {
		t.Fatalf("Model(llama3) = %+v, %v", m, ok)
	}
	if _, ok := i.Model("llama3:8b"); ok {
		t.Fatal("different tag must not match")
	}
}
