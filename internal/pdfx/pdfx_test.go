package pdfx

import (
	"image"
	"image/color"
	"math"
	"testing"
)

const eps = 1e-9

func approxEqual(a, b float64) bool { return math.Abs(a-b) < eps }

func approxEqualBox(a, b [4]float64) bool {
	for i := range a {
		if !approxEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestNormToPoints(t *testing.T) {
	tests := []struct {
		name         string
		norm         [4]float64
		pageW, pageH float64
		want         [4]float64
	}{
		{"unit page full", [4]float64{0, 0, 1, 1}, 1, 1, [4]float64{0, 0, 1, 1}},
		{"a4 full", [4]float64{0, 0, 1, 1}, 595, 842, [4]float64{0, 0, 595, 842}},
		{"a4 quarter", [4]float64{0.25, 0.25, 0.75, 0.75}, 595, 842, [4]float64{148.75, 210.5, 446.25, 631.5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normToPoints(tt.norm, tt.pageW, tt.pageH)
			if !approxEqualBox(got, tt.want) {
				t.Errorf("normToPoints(%v, %v, %v) = %v; want %v", tt.norm, tt.pageW, tt.pageH, got, tt.want)
			}
		})
	}
}

func TestPointsToNormRoundTrip(t *testing.T) {
	pageW, pageH := 595.0, 842.0
	tests := [][4]float64{
		{0, 0, 1, 1},
		{0.1, 0.2, 0.8, 0.9},
		{0.333, 0.444, 0.666, 0.777},
	}
	for _, norm := range tests {
		pts := normToPoints(norm, pageW, pageH)
		back := pointsToNorm(pts, pageW, pageH)
		if !approxEqualBox(back, norm) {
			t.Errorf("round-trip failed: %v -> %v -> %v", norm, pts, back)
		}
	}
}

func TestClipBboxToPagePoints(t *testing.T) {
	pageW, pageH := 100.0, 200.0
	tests := []struct {
		name     string
		in       [4]float64
		want     [4]float64
		wantErr  bool
	}{
		{"fully inside", [4]float64{10, 20, 30, 40}, [4]float64{10, 20, 30, 40}, false},
		{"clip left/bottom", [4]float64{-5, -10, 50, 100}, [4]float64{0, 0, 50, 100}, false},
		{"clip right/top", [4]float64{50, 100, 200, 300}, [4]float64{50, 100, 100, 200}, false},
		{"fully right of page", [4]float64{150, 50, 200, 100}, [4]float64{}, true},
		{"fully above page", [4]float64{10, 250, 50, 300}, [4]float64{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := clipBboxToPagePoints(tt.in, pageW, pageH)
			if (err != nil) != tt.wantErr {
				t.Fatalf("clipBboxToPagePoints err = %v; wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && !approxEqualBox(got, tt.want) {
				t.Errorf("clipBboxToPagePoints(%v) = %v; want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestBboxPointsToImageRect(t *testing.T) {
	// A 100x200 pt page rendered at scale=1 -> 100x200 px image.
	// Image bounds (0,0)-(100,200), top-left origin.
	imgBounds := image.Rect(0, 0, 100, 200)
	pageH := 200.0
	scale := 1.0

	// PDF bbox covering full page (0,0,100,200) should yield image rect (0,0,100,200).
	got := bboxPointsToImageRect([4]float64{0, 0, 100, 200}, pageH, scale, imgBounds)
	if got != image.Rect(0, 0, 100, 200) {
		t.Errorf("full page: got %v; want (0,0,100,200)", got)
	}

	// Top-left quadrant in PDF space is x=0..50, y=100..200 (top half).
	// In image space (top-left origin), that's x=0..50, y=0..100.
	got = bboxPointsToImageRect([4]float64{0, 100, 50, 200}, pageH, scale, imgBounds)
	if got != image.Rect(0, 0, 50, 100) {
		t.Errorf("top-left quad: got %v; want (0,0,50,100)", got)
	}

	// Bottom-right quadrant in PDF space: x=50..100, y=0..100 (bottom half).
	// In image space: x=50..100, y=100..200.
	got = bboxPointsToImageRect([4]float64{50, 0, 100, 100}, pageH, scale, imgBounds)
	if got != image.Rect(50, 100, 100, 200) {
		t.Errorf("bottom-right quad: got %v; want (50,100,100,200)", got)
	}

	// Scale=2: 50x100 pt -> 100x200 px.
	imgBounds2 := image.Rect(0, 0, 100, 200)
	got = bboxPointsToImageRect([4]float64{0, 0, 50, 100}, 100.0, 2.0, imgBounds2)
	if got != image.Rect(0, 0, 100, 200) {
		t.Errorf("scale=2 full: got %v; want (0,0,100,200)", got)
	}
}

func TestCopyRegion(t *testing.T) {
	// Build a 4x4 RGBA where each pixel's R = x + y*10 (unique per pixel).
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x + y*10), G: 0, B: 0, A: 255})
		}
	}

	got := copyRegion(src, image.Rect(1, 1, 3, 3))
	if got.Bounds() != image.Rect(0, 0, 2, 2) {
		t.Fatalf("copy bounds: got %v; want (0,0,2,2)", got.Bounds())
	}
	// Expect: pixel (0,0) of dst comes from src (1,1) which is 1+1*10=11.
	wantR := [2][2]uint8{
		{11, 12}, // dst row 0  <- src row 1
		{21, 22}, // dst row 1  <- src row 2
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			c := got.RGBAAt(x, y)
			if c.R != wantR[y][x] {
				t.Errorf("dst(%d,%d).R = %d; want %d", x, y, c.R, wantR[y][x])
			}
		}
	}
}
