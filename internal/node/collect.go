package node

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Hardware fills in the static hardware fields of Info for this machine.
// Every probe is best effort: a missing tool leaves the field zero.
func Hardware() Info {
	return Info{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CPUs:        runtime.NumCPU(),
		MemoryBytes: totalMemory(),
		GPUs:        nvidiaGPUs(),
	}
}

func totalMemory() uint64 {
	switch runtime.GOOS {
	case "darwin":
		out, err := run("sysctl", "-n", "hw.memsize")
		if err != nil {
			return 0
		}
		n, _ := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
		return n
	case "linux":
		f, err := os.Open("/proc/meminfo")
		if err != nil {
			return 0
		}
		defer f.Close()
		return parseMeminfo(bufio.NewScanner(f))
	case "windows":
		return windowsMemory()
	}
	return 0
}

// parseMeminfo reads the MemTotal line ("MemTotal:  16314156 kB").
func parseMeminfo(s *bufio.Scanner) uint64 {
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, _ := strconv.ParseUint(fields[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}

func nvidiaGPUs() []GPU {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil
	}
	out, err := run("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	if err != nil {
		return nil
	}
	return parseNvidiaSMI(out)
}

// parseNvidiaSMI parses lines like "NVIDIA GeForce RTX 3090, 24576" (MiB).
func parseNvidiaSMI(out string) []GPU {
	var gpus []GPU
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, mem, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		mib, err := strconv.ParseUint(strings.TrimSpace(mem), 10, 64)
		if err != nil {
			continue
		}
		gpus = append(gpus, GPU{Name: strings.TrimSpace(name), MemoryBytes: mib << 20})
	}
	return gpus
}

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
