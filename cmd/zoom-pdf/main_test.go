package main

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

func TestParseBbox(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    [4]float64
		wantErr bool
	}{
		{"full page", "0,0,1,1", [4]float64{0, 0, 1, 1}, false},
		{"fractional", "0.25,0.5,0.75,1", [4]float64{0.25, 0.5, 0.75, 1}, false},
		{"with spaces", " 0 , 0.1 , 0.2 , 0.3 ", [4]float64{0, 0.1, 0.2, 0.3}, false},
		{"too few", "1,2,3", [4]float64{}, true},
		{"too many", "0,0,1,1,2", [4]float64{}, true},
		{"non-numeric", "a,b,c,d", [4]float64{}, true},
		{"out of range high", "0,0,2,1", [4]float64{}, true},
		{"out of range low", "-0.1,0,1,1", [4]float64{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseBbox(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseBbox(%q) err = %v; wantErr=%v", tt.in, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			for i := range got {
				if math.Abs(got[i]-tt.want[i]) > 1e-9 {
					t.Errorf("parseBbox(%q)[%d] = %v; want %v", tt.in, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestWritePlain(t *testing.T) {
	p := output{
		PDFPath:      "sample.pdf",
		Page:         3,
		Bbox:         [4]float64{0, 0.5, 1, 1},
		DPI:          200,
		PageSizePt:   pageSizePt{Width: 595, Height: 842},
		ImageSize:    imageSize{Width: 833, Height: 421},
		PageRotation: 90,
		HasTextLayer: true,
		Rects: []rectJSON{
			{Text: "Hello world", Bbox: [4]float64{0.18, 0.78, 0.42, 0.81}},
			{Text: "line\twith\ttabs\nand newline", Bbox: [4]float64{0.18, 0.74, 0.55, 0.77}},
		},
	}
	var buf bytes.Buffer
	if err := writePlain(&buf, p); err != nil {
		t.Fatalf("writePlain err: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (1 header + 2 rects), got %d:\n%s", len(lines), buf.String())
	}
	wantHeader := "pdf=sample.pdf page=3 bbox=0,0.5,1,1 dpi=200 page_pt=595x842 image_px=833x421 rotation=90 text_layer=true"
	if lines[0] != wantHeader {
		t.Errorf("header mismatch\n got: %s\nwant: %s", lines[0], wantHeader)
	}
	if lines[1] != "0.18,0.78,0.42,0.81\tHello world" {
		t.Errorf("rect[0] mismatch: %q", lines[1])
	}
	// tabs and newlines inside text must be replaced with single spaces so each
	// rect stays on one line and the bbox\ttext split remains unambiguous.
	if lines[2] != "0.18,0.74,0.55,0.77\tline with tabs and newline" {
		t.Errorf("rect[1] (sanitized) mismatch: %q", lines[2])
	}
}

func TestWritePlainEmptyRects(t *testing.T) {
	// Scanned PDF case: no text layer, no rects — should still emit a header
	// line with text_layer=false and nothing else.
	p := output{
		PDFPath:      "scan.pdf",
		Page:         0,
		Bbox:         [4]float64{0, 0, 1, 1},
		DPI:          300,
		PageSizePt:   pageSizePt{Width: 100, Height: 200},
		ImageSize:    imageSize{Width: 10, Height: 20},
		PageRotation: 0,
		HasTextLayer: false,
		Rects:        nil,
	}
	var buf bytes.Buffer
	if err := writePlain(&buf, p); err != nil {
		t.Fatalf("writePlain err: %v", err)
	}
	got := buf.String()
	want := "pdf=scan.pdf page=0 bbox=0,0,1,1 dpi=300 page_pt=100x200 image_px=10x20 rotation=0 text_layer=false\n"
	if got != want {
		t.Errorf("empty-rects output mismatch\n got: %q\nwant: %q", got, want)
	}
}
