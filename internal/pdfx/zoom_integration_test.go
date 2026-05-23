//go:build integration

// Integration tests that actually drive pdfium against sample.pdf.
// Run with: go test -tags=integration ./...
//
// These are kept out of the default `go test ./...` run because:
//   1. They require sample.pdf to be present at the repo root.
//   2. They spin up the wasm runtime (a few seconds per Zoom call).
package pdfx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sample.pdf lives at the repo root (../../sample.pdf relative to this file).
func loadSamplePDF(t *testing.T) []byte {
	t.Helper()
	path, err := filepath.Abs("../../sample.pdf")
	if err != nil {
		t.Fatalf("resolve sample.pdf path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("sample.pdf not available at %s: %v", path, err)
	}
	return data
}

func TestZoomFullPage(t *testing.T) {
	pdf := loadSamplePDF(t)
	res, err := Zoom(Options{
		PDFData: pdf,
		Page:    0,
		Bbox:    [4]float64{0, 0, 1, 1},
		DPI:     150,
	})
	if err != nil {
		t.Fatalf("Zoom: %v", err)
	}

	if res.PageWidthPt <= 0 || res.PageHeightPt <= 0 {
		t.Errorf("page size should be positive, got %vx%v", res.PageWidthPt, res.PageHeightPt)
	}
	if !res.HasTextLayer {
		t.Error("sample.pdf is born-digital — expected has_text_layer = true")
	}
	if len(res.Chars) == 0 {
		t.Error("expected some chars in a full-page extraction")
	}
	if len(res.Rects) == 0 {
		t.Error("expected some rects in a full-page extraction")
	}
	if len(res.Rects) > len(res.Chars) {
		t.Errorf("rects (%d) shouldn't outnumber chars (%d)", len(res.Rects), len(res.Chars))
	}

	// All rect text should be non-empty.
	for i, r := range res.Rects {
		if strings.TrimSpace(r.Text) == "" {
			t.Errorf("rect %d has empty text", i)
		}
	}

	// Image dimensions should roughly match page_size * dpi/72.
	scale := 150.0 / 72.0
	expectedW := int(res.PageWidthPt * scale)
	gotW := res.Image.Bounds().Dx()
	// Allow ±2 px for rounding.
	if abs(gotW-expectedW) > 2 {
		t.Errorf("image width %d differs from expected %d (±2)", gotW, expectedW)
	}
}

func TestZoomSubregionShrinksCounts(t *testing.T) {
	pdf := loadSamplePDF(t)

	full, err := Zoom(Options{PDFData: pdf, Page: 0, Bbox: [4]float64{0, 0, 1, 1}, DPI: 100})
	if err != nil {
		t.Fatalf("full Zoom: %v", err)
	}
	sub, err := Zoom(Options{PDFData: pdf, Page: 0, Bbox: [4]float64{0.25, 0.25, 0.75, 0.75}, DPI: 100})
	if err != nil {
		t.Fatalf("sub Zoom: %v", err)
	}

	if len(sub.Chars) >= len(full.Chars) {
		t.Errorf("subregion chars (%d) should be < full chars (%d)", len(sub.Chars), len(full.Chars))
	}
	if len(sub.Chars) == 0 {
		t.Error("center quarter of sample.pdf should still have some chars")
	}

	// Subregion image must be smaller than full image.
	if sub.Image.Bounds().Dx() >= full.Image.Bounds().Dx() {
		t.Errorf("subregion image width should be < full image width")
	}
}

func TestZoomBboxNormalizedRange(t *testing.T) {
	pdf := loadSamplePDF(t)
	res, err := Zoom(Options{PDFData: pdf, Page: 0, Bbox: [4]float64{0, 0, 1, 1}, DPI: 100})
	if err != nil {
		t.Fatalf("Zoom: %v", err)
	}
	// bbox values returned to the caller should be in [0,1].
	for i, v := range res.BboxClipped {
		if v < 0 || v > 1 {
			t.Errorf("BboxClipped[%d] = %v outside [0,1]", i, v)
		}
	}
	// Per-char bbox should also be in [0,1] (chars live on the page).
	for i, c := range res.Chars {
		for j, v := range c.Bbox {
			if v < -0.001 || v > 1.001 {
				t.Errorf("char[%d].Bbox[%d] = %v outside [0,1]", i, j, v)
			}
		}
	}
}

func TestZoomInvalidPage(t *testing.T) {
	pdf := loadSamplePDF(t)
	_, err := Zoom(Options{PDFData: pdf, Page: 9999, Bbox: [4]float64{0, 0, 1, 1}, DPI: 100})
	if err == nil {
		t.Error("expected error for out-of-range page, got nil")
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
