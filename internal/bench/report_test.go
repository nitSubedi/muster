package bench

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func twoDevices(rtt time.Duration, through float64, secondGBs float64) Report {
	return Report{
		Devices: []Device{
			{Name: "laptop", MemReadGBs: 100, LLM: &LLMResult{Model: "a", SizeBytes: 4e9, DecodeTokS: 30, PrefillTokS: 600}},
			{Name: "box", MemReadGBs: 50, LLM: &LLMResult{Model: "b", SizeBytes: 4e9, DecodeTokS: secondGBs / 4, PrefillTokS: 600 * secondGBs / 120}},
		},
		Links: []Link{{From: "laptop", To: "box", RTT: Stats{Median: rtt, P90: rtt, Min: rtt}, ThroughMB: through}},
	}
}

func TestImplicationsWiFiWithWeakBox(t *testing.T) {
	r := twoDevices(57*time.Millisecond, 5, 1)
	got := strings.Join(r.Implications(), "\n")
	for _, want := range []string{"1.01× the work of laptop alone: no meaningful gain", "7B model layer by layer", "slower than one device", "so no real difference"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestImplicationsWiredEqualMachinesFavourSplittingBigModels(t *testing.T) {
	r := twoDevices(200*time.Microsecond, 110, 120)
	got := r.Implications()
	if !strings.Contains(got[0], "2.00×") {
		t.Errorf("batch line = %q", got[0])
	}
	if !strings.Contains(got[2], "70B") || !strings.Contains(got[2], "so faster") {
		t.Errorf("70B line = %q", got[2])
	}
	if !strings.Contains(got[3], "so faster") {
		t.Errorf("prompt line = %q", got[3])
	}
}

func TestRenderIncludesDevicesLinksAndErrors(t *testing.T) {
	r := twoDevices(time.Millisecond, 50, 60)
	r.Devices = append(r.Devices, Device{Name: "pi", MemReadGBs: 4, LLMError: "Ollama not reachable"})
	var b bytes.Buffer
	r.Render(&b)
	out := b.String()
	for _, want := range []string{"DEVICE", "laptop", "(Ollama not reachable)", "laptop → box", "WHAT THIS MEANS"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestImplicationsNeedTwoMeasuredDevices(t *testing.T) {
	r := Report{Devices: []Device{{Name: "solo", LLM: &LLMResult{SizeBytes: 1, DecodeTokS: 1, PrefillTokS: 1}}}}
	if r.Implications() != nil {
		t.Fatal("single device should produce no implications")
	}
}

func TestBusyDeviceFlagged(t *testing.T) {
	r := twoDevices(time.Millisecond, 50, 60)
	r.Devices[1].CPUs, r.Devices[1].Load1 = 4, 31.7
	var b bytes.Buffer
	r.Render(&b)
	if !strings.Contains(b.String(), "box is busy with other work (load 31.7 on 4 CPUs)") {
		t.Fatalf("busy warning missing:\n%s", b.String())
	}
	if (Device{CPUs: 8, Load1: 2}).Busy() {
		t.Fatal("lightly loaded device flagged busy")
	}
}
