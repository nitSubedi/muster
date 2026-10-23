// Package lab runs first-principles experiments on splitting raw
// multiply-add work across machines. Everything a model does reduces to
// matrix multiplications, and two shapes matter:
//
//   - one big independent multiplication (prompt reading, batches, images):
//     lots of work per byte sent and no waiting, so it can split well;
//   - a chain of small dependent steps (generating a token): every step needs
//     the previous answer, so each split step pays a network round trip.
//
// The worker keeps its matrix resident, as a device keeps model weights,
// so only activations cross the network.
package lab

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"runtime"
	"sync"
)

// Mat is a row-major float32 matrix.
type Mat struct {
	Rows, Cols int
	Data       []float32
}

// NewMat allocates a rows×cols matrix.
func NewMat(rows, cols int) Mat { return Mat{rows, cols, make([]float32, rows*cols)} }

// Fill sets deterministic pseudo-random values in [-1, 1).
func (m Mat) Fill(seed uint32) {
	x := seed | 1
	for i := range m.Data {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		m.Data[i] = float32(x)/float32(math.MaxUint32)*2 - 1
	}
}

// Row returns row i.
func (m Mat) Row(i int) []float32 { return m.Data[i*m.Cols : (i+1)*m.Cols] }

// MulInto computes out = a × b using every core. out must be a.Rows×b.Cols.
func MulInto(out, a, b Mat) {
	parallelRows(a.Rows, func(lo, hi int) {
		for i := lo; i < hi; i++ {
			ci := out.Row(i)
			clear(ci)
			ai := a.Row(i)
			for k, av := range ai {
				bk := b.Row(k)
				for j := range ci {
					ci[j] += av * bk[j]
				}
			}
		}
	})
}

// MatVecRows computes out[i-lo] = w.Row(i) · x for rows lo..hi.
func MatVecRows(out []float32, w Mat, lo, hi int, x []float32) {
	parallelRows(hi-lo, func(a, b int) {
		for i := a; i < b; i++ {
			row := w.Row(lo + i)
			var s float32
			for j, v := range row {
				s += v * x[j]
			}
			out[i] = s
		}
	})
}

func parallelRows(n int, f func(lo, hi int)) {
	workers := min(runtime.NumCPU(), n)
	if workers <= 1 {
		f(0, n)
		return
	}
	var wg sync.WaitGroup
	chunk := (n + workers - 1) / workers
	for lo := 0; lo < n; lo += chunk {
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			f(lo, hi)
		}(lo, min(lo+chunk, n))
	}
	wg.Wait()
}

// Normalize scales x to unit length, so long chains stay finite.
func Normalize(x []float32) {
	var s float64
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	if s == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(s))
	for i := range x {
		x[i] *= inv
	}
}

// RelDiff is the largest difference relative to the largest magnitude.
// Different CPUs round multiply-adds differently (ARM fuses them into one
// rounding step, x86 baseline code rounds twice), so results from two
// machines agree to within float32 precision, not bit for bit.
func RelDiff(a, b []float32) float64 {
	var m float64
	for _, v := range b {
		m = max(m, math.Abs(float64(v)))
	}
	if m == 0 {
		return MaxAbsDiff(a, b)
	}
	return MaxAbsDiff(a, b) / m
}

// Agree reports whether two results match to float32 working precision.
func Agree(a, b []float32) bool { return RelDiff(a, b) < 1e-5 }

// MaxAbsDiff compares two equal-length slices.
func MaxAbsDiff(a, b []float32) float64 {
	var d float64
	for i := range a {
		d = max(d, math.Abs(float64(a[i]-b[i])))
	}
	return d
}

// Wire protocol: 1-byte op, then little-endian uint32 sizes and float32 data.
const (
	opPing      byte = 1
	opSetMatrix byte = 2 // rows, cols, data: becomes the resident matrix
	opMulRows   byte = 3 // rows, data (rows×residentRows): returns rows×residentCols
	opMatVec    byte = 4 // lo, hi, x[cols]: returns hi-lo values
)

// Serve answers one client connection until it closes.
func Serve(conn net.Conn) error {
	defer conn.Close()
	r := bufio.NewReaderSize(conn, 1<<20)
	w := bufio.NewWriterSize(conn, 1<<20)
	var resident Mat
	var u32 [3]uint32
	for {
		op, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch op {
		case opPing:
			w.WriteByte(opPing)
		case opSetMatrix:
			if err := binary.Read(r, binary.LittleEndian, u32[:2]); err != nil {
				return err
			}
			if u32[0]*u32[1] > 1<<28 {
				return errors.New("matrix too large")
			}
			resident = NewMat(int(u32[0]), int(u32[1]))
			if err := binary.Read(r, binary.LittleEndian, resident.Data); err != nil {
				return err
			}
			w.WriteByte(opSetMatrix)
		case opMulRows:
			if err := binary.Read(r, binary.LittleEndian, u32[:1]); err != nil {
				return err
			}
			a := NewMat(int(u32[0]), resident.Rows)
			if err := binary.Read(r, binary.LittleEndian, a.Data); err != nil {
				return err
			}
			out := NewMat(a.Rows, resident.Cols)
			MulInto(out, a, resident)
			if err := binary.Write(w, binary.LittleEndian, out.Data); err != nil {
				return err
			}
		case opMatVec:
			if err := binary.Read(r, binary.LittleEndian, u32[:2]); err != nil {
				return err
			}
			lo, hi := int(u32[0]), int(u32[1])
			if lo < 0 || hi > resident.Rows || lo > hi {
				return errors.New("bad row range")
			}
			x := make([]float32, resident.Cols)
			if err := binary.Read(r, binary.LittleEndian, x); err != nil {
				return err
			}
			out := make([]float32, hi-lo)
			MatVecRows(out, resident, lo, hi, x)
			if err := binary.Write(w, binary.LittleEndian, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown op %d", op)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
}

// Client talks to one worker.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	// residentRows/Cols mirror the worker's matrix shape.
	residentRows, residentCols int
	// BytesSent and BytesReceived count payload crossing the network.
	BytesSent, BytesReceived int64
}

// Dial connects to a worker.
func Dial(addr string) (*Client, error) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	return &Client{conn: c, r: bufio.NewReaderSize(c, 1<<20), w: bufio.NewWriterSize(c, 1<<20)}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) expect(op byte) error {
	got, err := c.r.ReadByte()
	if err != nil {
		return err
	}
	if got != op {
		return fmt.Errorf("worker replied %d, want %d", got, op)
	}
	return nil
}

// Ping does one round trip with no work.
func (c *Client) Ping() error {
	c.w.WriteByte(opPing)
	if err := c.w.Flush(); err != nil {
		return err
	}
	return c.expect(opPing)
}

// SetMatrix uploads the worker's resident matrix (like loading weights).
func (c *Client) SetMatrix(m Mat) error {
	c.w.WriteByte(opSetMatrix)
	binary.Write(c.w, binary.LittleEndian, []uint32{uint32(m.Rows), uint32(m.Cols)})
	binary.Write(c.w, binary.LittleEndian, m.Data)
	if err := c.w.Flush(); err != nil {
		return err
	}
	c.residentRows, c.residentCols = m.Rows, m.Cols
	return c.expect(opSetMatrix)
}

// MulRows sends a (rows × residentRows) and returns a × resident.
func (c *Client) MulRows(a Mat) (Mat, error) {
	c.w.WriteByte(opMulRows)
	binary.Write(c.w, binary.LittleEndian, uint32(a.Rows))
	binary.Write(c.w, binary.LittleEndian, a.Data)
	if err := c.w.Flush(); err != nil {
		return Mat{}, err
	}
	c.BytesSent += int64(4 * len(a.Data))
	out := NewMat(a.Rows, c.residentCols)
	err := binary.Read(c.r, binary.LittleEndian, out.Data)
	c.BytesReceived += int64(4 * len(out.Data))
	return out, err
}

// MatVec asks for rows lo..hi of resident × x.
func (c *Client) MatVec(lo, hi int, x []float32) ([]float32, error) {
	c.w.WriteByte(opMatVec)
	binary.Write(c.w, binary.LittleEndian, []uint32{uint32(lo), uint32(hi)})
	binary.Write(c.w, binary.LittleEndian, x)
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	c.BytesSent += int64(4 * len(x))
	out := make([]float32, hi-lo)
	err := binary.Read(c.r, binary.LittleEndian, out)
	c.BytesReceived += int64(4 * len(out))
	return out, err
}
