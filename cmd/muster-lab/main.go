// Command muster-lab runs first-principles experiments on splitting raw
// multiply-add work between this machine and a worker machine.
//
//	worker machine:  muster-lab worker --listen :7878 --allow <runner-ip>
//	this machine:    muster-lab run --worker <worker-ip>:7878
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"sort"
	"time"

	"github.com/nitSubedi/muster/internal/lab"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: muster-lab worker|run [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "worker":
		err = worker(os.Args[2:])
	case "run":
		err = run(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func worker(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	listen := fs.String("listen", ":7878", "address to listen on")
	allow := fs.String("allow", "", "only accept connections from this IP (required)")
	minutes := fs.Int("minutes", 15, "exit after this many minutes")
	fs.Parse(args)
	allowed, err := netip.ParseAddr(*allow)
	if err != nil {
		return fmt.Errorf("--allow must be the runner's IP: %w", err)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	log.Printf("worker on %s, accepting only %s, exiting in %d min", *listen, allowed, *minutes)
	time.AfterFunc(time.Duration(*minutes)*time.Minute, func() { log.Print("time limit reached"); os.Exit(0) })
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		ap, _ := netip.ParseAddrPort(c.RemoteAddr().String())
		if ap.Addr().Unmap() != allowed {
			log.Printf("rejected %s", c.RemoteAddr())
			c.Close()
			continue
		}
		go func() {
			if err := lab.Serve(c); err != nil {
				log.Printf("session: %v", err)
			}
		}()
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	addr := fs.String("worker", "", "worker host:port (required)")
	n := fs.Int("n", 2048, "experiment 1: size of the square matrices")
	dim := fs.Int("dim", 3584, "experiment 2: hidden size (7B models use 3584)")
	steps := fs.Int("steps", 200, "experiment 2: dependent steps (≈ one token of a 7B model)")
	fs.Parse(args)

	c, err := lab.Dial(*addr)
	if err != nil {
		return err
	}
	defer c.Close()

	// Network round trip with no work.
	var rtts []time.Duration
	for i := 0; i < 31; i++ {
		t := time.Now()
		if err := c.Ping(); err != nil {
			return err
		}
		if i > 0 {
			rtts = append(rtts, time.Since(t))
		}
	}
	sort.Slice(rtts, func(i, j int) bool { return rtts[i] < rtts[j] })
	rtt := rtts[len(rtts)/2]
	fmt.Printf("Network round trip to %s: %s median (min %s, max %s)\n\n", *addr, ms(rtt), ms(rtts[0]), ms(rtts[len(rtts)-1]))

	f, err := experiment1(c, *n)
	if err != nil {
		return err
	}
	return experiment2(c, *dim, *steps, f, rtt)
}

// experiment1: one big independent multiplication, C = A × B. B stays
// resident on the worker (like weights); A's rows are split by speed.
// Returns the local share used.
func experiment1(c *lab.Client, n int) (float64, error) {
	flops := 2 * float64(n) * float64(n) * float64(n)
	fmt.Printf("EXPERIMENT 1: one big independent multiplication (%d×%d by %d×%d = %.1f GFLOP)\n", n, n, n, n, flops/1e9)
	fmt.Println("  Like reading a long prompt, a batch of requests, or one image-generation step.")
	a, b := lab.NewMat(n, n), lab.NewMat(n, n)
	a.Fill(1)
	b.Fill(2)

	t := time.Now()
	if err := c.SetMatrix(b); err != nil {
		return 0, err
	}
	fmt.Printf("  Loading the resident matrix onto the worker (once, like loading weights): %.0f MB in %s\n", float64(n*n*4)/1e6, ms(time.Since(t)))

	full := lab.NewMat(n, n)
	t = time.Now()
	lab.MulInto(full, a, b)
	local := time.Since(t)

	t = time.Now()
	remoteFull, err := c.MulRows(a)
	if err != nil {
		return 0, err
	}
	remote := time.Since(t)
	if !lab.Agree(remoteFull.Data, full.Data) {
		return 0, fmt.Errorf("worker result differs (relative %.2g)", lab.RelDiff(remoteFull.Data, full.Data))
	}
	fmt.Printf("  This machine alone:  %8s  (%.1f GFLOP/s)\n", ms(local), flops/local.Seconds()/1e9)
	fmt.Printf("  Worker alone:        %8s  (%.1f GFLOP/s, including sending the data)\n", ms(remote), flops/remote.Seconds()/1e9)

	share := remote.Seconds() / (local.Seconds() + remote.Seconds()) // local's share of rows
	remoteRows := int(float64(n)*(1-share) + 0.5)
	if remoteRows == 0 {
		fmt.Println("  Split: the worker is too slow to be given even one row.")
		return 1, nil
	}
	localRows := n - remoteRows
	sent0, recv0 := c.BytesSent, c.BytesReceived
	aRemote := lab.Mat{Rows: remoteRows, Cols: n, Data: a.Data[localRows*n:]}
	aLocal := lab.Mat{Rows: localRows, Cols: n, Data: a.Data[:localRows*n]}
	outLocal := lab.NewMat(localRows, n)

	t = time.Now()
	done := make(chan error, 1)
	var outRemote lab.Mat
	go func() {
		var err error
		outRemote, err = c.MulRows(aRemote)
		done <- err
	}()
	lab.MulInto(outLocal, aLocal, b)
	if err := <-done; err != nil {
		return 0, err
	}
	split := time.Since(t)

	combined := append(append([]float32(nil), outLocal.Data...), outRemote.Data...)
	if !lab.Agree(combined, full.Data) {
		return 0, fmt.Errorf("split result differs (relative %.2g)", lab.RelDiff(combined, full.Data))
	}
	fmt.Printf("  Split by speed (%d%% here / %d%% worker): %s  -> %s  (answer verified, max relative difference %.1g)\n",
		localRows*100/n, remoteRows*100/n, ms(split), verdict(local, split), lab.RelDiff(combined, full.Data))
	fmt.Printf("  Network traffic for the split: %.1f MB sent, %.1f MB received, in 1 round trip\n\n",
		float64(c.BytesSent-sent0)/1e6, float64(c.BytesReceived-recv0)/1e6)
	return share, nil
}

// experiment2: a chain of dependent matrix-vector steps, each needing the
// previous result: what generating one token looks like.
func experiment2(c *lab.Client, dim, steps int, share float64, rtt time.Duration) error {
	flops := 2 * float64(dim) * float64(dim) * float64(steps)
	fmt.Printf("EXPERIMENT 2: a chain of %d dependent steps (%d×%d matrix times a vector each = %.1f GFLOP)\n", steps, dim, dim, flops/1e9)
	fmt.Println("  Like generating ONE token: each step needs the previous step's answer.")
	w := lab.NewMat(dim, dim)
	w.Fill(3)
	t := time.Now()
	if err := c.SetMatrix(w); err != nil {
		return err
	}
	fmt.Printf("  Loading the resident matrix onto the worker (once): %.0f MB in %s\n", float64(dim*dim*4)/1e6, ms(time.Since(t)))

	x0 := make([]float32, dim)
	x0[0] = 1

	x := append([]float32(nil), x0...)
	y := make([]float32, dim)
	t = time.Now()
	for s := 0; s < steps; s++ {
		lab.MatVecRows(y, w, 0, dim, x)
		lab.Normalize(y)
		x, y = y, x
	}
	local := time.Since(t)
	want := append([]float32(nil), x...)

	remoteRows := int(float64(dim)*(1-share) + 0.5)
	remoteRows = max(remoteRows, 1) // always split, to measure the cost
	localRows := dim - remoteRows
	x = append([]float32(nil), x0...)
	t = time.Now()
	for s := 0; s < steps; s++ {
		done := make(chan error, 1)
		var part []float32
		go func() {
			var err error
			part, err = c.MatVec(localRows, dim, x)
			done <- err
		}()
		lab.MatVecRows(y[:localRows], w, 0, localRows, x)
		if err := <-done; err != nil {
			return err
		}
		copy(y[localRows:], part)
		lab.Normalize(y)
		x, y = y, x
	}
	split := time.Since(t)
	if !lab.Agree(x, want) {
		return fmt.Errorf("split chain result differs (relative %.2g)", lab.RelDiff(x, want))
	}
	fmt.Printf("  This machine alone:  %8s  (%s per step)\n", ms(local), ms(local/time.Duration(steps)))
	fmt.Printf("  Split (%d%% here / %d%% worker): %s  (%s per step)  -> %s  (answer verified, max relative difference %.1g)\n",
		localRows*100/dim, remoteRows*100/dim, ms(split), ms(split/time.Duration(steps)), verdict(local, split), lab.RelDiff(x, want))
	fmt.Printf("  The split waited on the network %d times; %d × %s round trip = %s of pure waiting.\n\n",
		steps, steps, ms(rtt), ms(rtt*time.Duration(steps)))
	return nil
}

func verdict(alone, split time.Duration) string {
	r := alone.Seconds() / split.Seconds()
	switch {
	case r > 1.05:
		return fmt.Sprintf("%.2f× FASTER", r)
	case r < 0.95:
		return fmt.Sprintf("%.1f× SLOWER", 1/r)
	}
	return "no real difference"
}

func ms(d time.Duration) string {
	v := float64(d) / float64(time.Millisecond)
	switch {
	case v < 10:
		return fmt.Sprintf("%.2f ms", v)
	case v < 10000:
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.1f s", v/1000)
}
