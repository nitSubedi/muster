package plan

import (
	"strings"
	"testing"
	"time"

	"github.com/nitSubedi/muster/internal/bench"
	"github.com/nitSubedi/muster/internal/node"
)

const gb = 1_000_000_000

func m(name string, size int64) node.Model { return node.Model{Name: name, Size: size} }

func measured(name string, cpus int, load, effGBs float64) bench.Device {
	return bench.Device{Name: name, CPUs: cpus, Load1: load,
		LLM: &bench.LLMResult{Model: "x", SizeBytes: gb, DecodeTokS: effGBs, PrefillTokS: 10 * effGBs}}
}

func live(name string, cpus int, load float64, mem uint64, models ...node.Model) Device {
	return Device{Name: name, Healthy: true, BackendUp: true, CPUs: cpus, Load1: load, MemoryBytes: mem, Models: models}
}

func roles(p Plan) map[string][]string {
	out := map[string][]string{}
	for _, a := range p.Assignments {
		out[a.Role] = append(out[a.Role], a.Device)
	}
	return out
}

func find(p Plan, role, device string) Assignment {
	for _, a := range p.Assignments {
		if a.Role == role && a.Device == device {
			return a
		}
	}
	return Assignment{}
}

// The field-test cluster: a fast laptop and an old, overloaded Linux box.
func TestFieldClusterLaptopDoesEverythingBoxOnStandby(t *testing.T) {
	now := time.Now()
	p := Make(Input{
		Now:   now,
		Bench: bench.Report{MeasuredAt: now.Add(-time.Hour), Devices: []bench.Device{measured("laptop", 11, 3, 132.8), measured("linuxbox", 4, 31.7, 0.5)}},
		Live: []Device{
			live("laptop", 11, 3, 18<<30, m("qwen2.5:7b", 4.7*gb), m("qwen2.5:3b", 1.9*gb), m("nomic-embed-text:latest", 0.27*gb)),
			live("linuxbox", 4, 31.7, 7<<30, m("qwen2.5:0.5b", 0.4*gb), m("llama3.2:3b", 2*gb)),
		},
	})
	r := roles(p)
	if got := r["chat"]; len(got) != 1 || got[0] != "laptop" {
		t.Fatalf("chat = %v", got)
	}
	if !strings.Contains(find(p, "chat", "laptop").Detail, "qwen2.5:7b") {
		t.Errorf("chat should use the largest model that stays readable: %q", find(p, "chat", "laptop").Detail)
	}
	if got := r["embeddings"]; len(got) != 1 || got[0] != "laptop" || !strings.Contains(find(p, "embeddings", "laptop").Detail, "shares") {
		t.Errorf("embeddings = %v %q", got, find(p, "embeddings", "laptop").Detail)
	}
	if got := r["batch"]; len(got) != 1 || got[0] != "laptop" {
		t.Errorf("batch = %v (linuxbox adds <5%%)", got)
	}
	sb := find(p, "standby", "linuxbox")
	if !strings.Contains(sb.Detail, "busy") || !strings.Contains(sb.Detail, "31.7") {
		t.Errorf("standby detail = %q", sb.Detail)
	}
	if !strings.Contains(strings.Join(p.Suggestions, "\n"), "linuxbox") {
		t.Errorf("suggestions should tell the user how to get value from linuxbox: %v", p.Suggestions)
	}
}

// Two capable, idle machines: chat on the stronger, embeddings moved off it,
// batch split by capacity.
func TestTwoCapableMachinesSplitRolesAndBatch(t *testing.T) {
	now := time.Now()
	p := Make(Input{
		Now:   now,
		Bench: bench.Report{MeasuredAt: now, Devices: []bench.Device{measured("desktop", 16, 1, 300), measured("laptop", 10, 1, 100)}},
		Live: []Device{
			live("desktop", 16, 1, 64<<30, m("qwen2.5:32b", 19*gb), m("qwen2.5:7b", 4.7*gb)),
			live("laptop", 10, 1, 16<<30, m("qwen2.5:7b", 4.7*gb), m("nomic-embed-text", 0.27*gb)),
		},
	})
	if a := find(p, "chat", "desktop"); !strings.Contains(a.Detail, "qwen2.5:32b") {
		t.Fatalf("chat should be the 32B on desktop (300/19 ≈ 15.8 tok/s): %+v", p.Assignments)
	}
	if roles(p)["embeddings"][0] != "laptop" {
		t.Errorf("embeddings should move off the chat device: %v", roles(p)["embeddings"])
	}
	if d := find(p, "batch", "desktop").Detail; !strings.Contains(d, "75%") {
		t.Errorf("desktop batch share = %q, want 75%%", d)
	}
	if d := find(p, "batch", "laptop").Detail; !strings.Contains(d, "25%") {
		t.Errorf("laptop batch share = %q, want 25%%", d)
	}
}

func TestSuggestsPullingEmbeddingModelOnFreeDevice(t *testing.T) {
	now := time.Now()
	p := Make(Input{
		Now:   now,
		Bench: bench.Report{MeasuredAt: now, Devices: []bench.Device{measured("desktop", 16, 1, 300), measured("mini", 8, 1, 60)}},
		Live: []Device{
			live("desktop", 16, 1, 64<<30, m("qwen2.5:7b", 4.7*gb), m("nomic-embed-text", 0.27*gb)),
			live("mini", 8, 1, 16<<30, m("qwen2.5:3b", 1.9*gb)),
		},
	})
	if !strings.Contains(strings.Join(p.Suggestions, "\n"), "ollama pull nomic-embed-text") {
		t.Fatalf("expected a pull suggestion for mini: %v", p.Suggestions)
	}
}

func TestLoadAdjustmentUsesLiveLoad(t *testing.T) {
	// Measured while busy (load 3.6 of 4 => 10% free); now idle => ~10x capacity.
	got := capacityNow(bench.Device{CPUs: 4, Load1: 3.6, LLM: &bench.LLMResult{SizeBytes: gb, DecodeTokS: 1}}, Device{CPUs: 4, Load1: 0})
	if got < 9.9 || got > 10.1 {
		t.Fatalf("capacity now = %v GB/s, want ~10", got)
	}
}

func TestStandbyReasons(t *testing.T) {
	now := time.Now()
	p := Make(Input{
		Now:   now,
		Bench: bench.Report{MeasuredAt: now, Devices: []bench.Device{measured("laptop", 10, 1, 100)}},
		Live: []Device{
			live("laptop", 10, 1, 16<<30, m("qwen2.5:7b", 4.7*gb)),
			{Name: "pi", Healthy: true, BackendUp: false, CPUs: 4},
			live("newbox", 8, 0, 32<<30, m("qwen2.5:7b", 4.7*gb)), // never benchmarked
			{Name: "nas", Healthy: false},
		},
	})
	for dev, want := range map[string]string{"pi": "Ollama", "newbox": "muster bench", "nas": "unreachable"} {
		if d := find(p, "standby", dev).Detail; !strings.Contains(d, want) {
			t.Errorf("%s standby detail = %q, want mention of %q", dev, d, want)
		}
	}
}

func TestNoModelFastEnoughForChat(t *testing.T) {
	now := time.Now()
	p := Make(Input{
		Now:   now,
		Bench: bench.Report{MeasuredAt: now, Devices: []bench.Device{measured("slow", 4, 0, 5)}},
		Live:  []Device{live("slow", 4, 0, 8<<30, m("qwen2.5:7b", 4.7*gb))},
	})
	if len(roles(p)["chat"]) != 0 {
		t.Fatalf("chat assigned to a device that runs at ~1 tok/s: %+v", p.Assignments)
	}
	if !strings.Contains(strings.Join(p.Suggestions, "\n"), "10 tok/s") {
		t.Errorf("suggestions should explain why chat is unassigned: %v", p.Suggestions)
	}
}

func TestModelMustFitInMemory(t *testing.T) {
	now := time.Now()
	p := Make(Input{
		Now:   now,
		Bench: bench.Report{MeasuredAt: now, Devices: []bench.Device{measured("laptop", 10, 0, 500)}},
		Live:  []Device{live("laptop", 10, 0, 16<<30, m("llama3.3:70b", 40*gb), m("qwen2.5:7b", 4.7*gb))},
	})
	if d := find(p, "chat", "laptop").Detail; !strings.Contains(d, "qwen2.5:7b") {
		t.Fatalf("a 40 GB model can't be the chat model on a 16 GB device: %q", d)
	}
}

func TestStaleMeasurementsFlagged(t *testing.T) {
	now := time.Now()
	p := Make(Input{
		Now:   now,
		Bench: bench.Report{MeasuredAt: now.Add(-72 * time.Hour), Devices: []bench.Device{measured("laptop", 10, 0, 100)}},
		Live:  []Device{live("laptop", 10, 0, 16<<30, m("qwen2.5:7b", 4.7*gb))},
	})
	if !strings.Contains(strings.Join(p.Suggestions, "\n"), "3 days old") {
		t.Fatalf("suggestions = %v", p.Suggestions)
	}
}

// Field bug: an idle but very slow device (0.4% of capacity) was suggested
// for embeddings. A role needs a real share of the cluster's capacity.
func TestSlowDeviceNotSuggestedForEmbeddings(t *testing.T) {
	now := time.Now()
	p := Make(Input{
		Now:   now,
		Bench: bench.Report{MeasuredAt: now, Devices: []bench.Device{measured("laptop", 11, 1, 115), measured("linuxbox", 4, 0, 0.4)}},
		Live: []Device{
			live("laptop", 11, 1, 18<<30, m("qwen2.5:7b", 4.7*gb), m("nomic-embed-text", 0.27*gb)),
			live("linuxbox", 4, 0, 7<<30, m("qwen2.5:0.5b", 0.4*gb)),
		},
	})
	if s := strings.Join(p.Suggestions, "\n"); strings.Contains(s, "pull nomic-embed-text") {
		t.Fatalf("suggested embeddings on a device with 0.4%% of capacity: %v", p.Suggestions)
	}
	if d := find(p, "embeddings", "laptop").Detail; strings.Contains(d, "free enough") {
		t.Errorf("detail should not blame CPU when the other device is just too slow: %q", d)
	}
}

func TestPctFormatting(t *testing.T) {
	if pct(0.0004) != "under 0.1%" || pct(0.0412) != "4.1%" {
		t.Fatalf("pct = %q, %q", pct(0.0004), pct(0.0412))
	}
}
