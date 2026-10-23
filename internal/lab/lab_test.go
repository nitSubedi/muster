package lab

import (
	"net"
	"testing"
)

func naiveMul(a, b Mat) Mat {
	out := NewMat(a.Rows, b.Cols)
	for i := 0; i < a.Rows; i++ {
		for j := 0; j < b.Cols; j++ {
			var s float32
			for k := 0; k < a.Cols; k++ {
				s += a.Data[i*a.Cols+k] * b.Data[k*b.Cols+j]
			}
			out.Data[i*out.Cols+j] = s
		}
	}
	return out
}

func TestMulIntoMatchesNaive(t *testing.T) {
	a, b := NewMat(37, 23), NewMat(23, 41)
	a.Fill(1)
	b.Fill(2)
	got := NewMat(37, 41)
	MulInto(got, a, b)
	if d := MaxAbsDiff(got.Data, naiveMul(a, b).Data); d > 1e-4 {
		t.Fatalf("max diff %v", d)
	}
}

func TestMatVecRows(t *testing.T) {
	w := NewMat(10, 6)
	w.Fill(3)
	x := make([]float32, 6)
	for i := range x {
		x[i] = float32(i)
	}
	out := make([]float32, 4)
	MatVecRows(out, w, 3, 7, x)
	for i := 0; i < 4; i++ {
		var s float32
		for j, v := range w.Row(3 + i) {
			s += v * x[j]
		}
		if d := s - out[i]; d > 1e-5 || d < -1e-5 {
			t.Fatalf("row %d: %v vs %v", 3+i, out[i], s)
		}
	}
}

// The split computed by a worker must equal the local result exactly
// enough that a speedup can never come from a wrong answer.
func TestWorkerProtocolMatchesLocal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go Serve(c)
		}
	}()
	c, err := Dial(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Ping(); err != nil {
		t.Fatal(err)
	}

	b := NewMat(64, 48)
	b.Fill(7)
	if err := c.SetMatrix(b); err != nil {
		t.Fatal(err)
	}
	a := NewMat(20, 64)
	a.Fill(9)
	remote, err := c.MulRows(a)
	if err != nil {
		t.Fatal(err)
	}
	local := NewMat(20, 48)
	MulInto(local, a, b)
	if d := MaxAbsDiff(remote.Data, local.Data); d != 0 {
		t.Fatalf("remote multiply differs by %v", d)
	}
	if c.BytesSent != 20*64*4 || c.BytesReceived != 20*48*4 {
		t.Fatalf("byte counts %d/%d", c.BytesSent, c.BytesReceived)
	}

	w := NewMat(64, 64)
	w.Fill(11)
	c.SetMatrix(w)
	x := make([]float32, 64)
	x[0] = 1
	part, err := c.MatVec(10, 30, x)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]float32, 20)
	MatVecRows(want, w, 10, 30, x)
	if MaxAbsDiff(part, want) != 0 {
		t.Fatal("remote matvec differs")
	}
}

func TestNormalize(t *testing.T) {
	x := []float32{3, 4}
	Normalize(x)
	if d := MaxAbsDiff(x, []float32{0.6, 0.8}); d > 1e-6 {
		t.Fatalf("got %v", x)
	}
}

func TestAgreeToleratesRoundingNotErrors(t *testing.T) {
	a := []float32{15, -3, 7}
	if !Agree([]float32{15.00003, -3, 7}, a) {
		t.Fatal("rounding-level difference rejected")
	}
	if Agree([]float32{15.1, -3, 7}, a) {
		t.Fatal("real error accepted")
	}
}
