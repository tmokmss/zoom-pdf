//go:build integration

// Integration tests that actually drive pdfium against testdata/sample.pdf.
// Run with: go test -tags=integration ./...
//
// These are kept out of the default `go test ./...` run because they spin
// up the wasm runtime (a few seconds per Zoom call).
package pdfx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sample.pdf lives at testdata/sample.pdf (../../testdata/sample.pdf relative to this file).
func loadSamplePDF(t *testing.T) []byte {
	t.Helper()
	path, err := filepath.Abs("../../testdata/sample.pdf")
	if err != nil {
		t.Fatalf("resolve sample.pdf path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("testdata/sample.pdf not available at %s: %v", path, err)
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
		t.Error("testdata/sample.pdf is born-digital — expected has_text_layer = true")
	}
	if len(res.Rects) == 0 {
		t.Error("expected some rects in a full-page extraction")
	}

	// All rect text should be non-empty.
	for i, r := range res.Rects {
		if strings.TrimSpace(r.Text) == "" {
			t.Errorf("rect %d has empty text", i)
		}
	}

	// Concatenated text should contain known content from the generated PDF.
	joined := joinRectText(res.Rects)
	for _, want := range []string{"Chapter One", "Lorem ipsum"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected extracted text to contain %q, got: %q", want, joined)
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

	if len(sub.Rects) >= len(full.Rects) {
		t.Errorf("subregion rects (%d) should be < full rects (%d)", len(sub.Rects), len(full.Rects))
	}
	if len(sub.Rects) == 0 {
		t.Error("center quarter of testdata/sample.pdf should still have some rects")
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
	// Per-rect bbox should also be in [0,1] (rects live on the page).
	for i, r := range res.Rects {
		for j, v := range r.Bbox {
			if v < -0.001 || v > 1.001 {
				t.Errorf("rect[%d].Bbox[%d] = %v outside [0,1]", i, j, v)
			}
		}
	}
}

// Each page in the generated PDF starts with a distinct chapter heading,
// so we can sanity-check that Page indexing actually selects the right page.
func TestZoomPageIndexing(t *testing.T) {
	pdf := loadSamplePDF(t)
	cases := []struct {
		page int
		want string
	}{
		{0, "Chapter One"},
		{1, "Chapter Two"},
		{2, "Chapter Three"},
	}
	for _, tc := range cases {
		res, err := Zoom(Options{PDFData: pdf, Page: tc.page, Bbox: [4]float64{0, 0, 1, 1}, DPI: 100})
		if err != nil {
			t.Errorf("page %d: Zoom: %v", tc.page, err)
			continue
		}
		joined := joinRectText(res.Rects)
		if !strings.Contains(joined, tc.want) {
			t.Errorf("page %d: expected text to contain %q, got: %q", tc.page, tc.want, joined)
		}
	}
}

// Chars on a line that extend past the requested bbox should be dropped at
// the character level — not the whole rect kept the way the old line-level
// filter did. We isolate the "Chapter One: Introduction" heading with a
// narrow y-band, then clip it on the right at x=0.3:
//   - narrow text must be a strict prefix of the full heading (i.e. only the
//     trailing chars outside the bbox got dropped)
//   - narrow rect right edge must be < full rect right edge (proves cropping)
//   - narrow right edge must sit between 0.3 and ~one char-width past it
//     (overlap criterion: the char straddling x=0.3 is kept, not dropped)
func TestZoomCharLevelBboxCrop(t *testing.T) {
	pdf := loadSamplePDF(t)

	const yLo, yHi = 0.895, 0.925 // y-band around the Chapter One heading
	full, err := Zoom(Options{PDFData: pdf, Page: 0, Bbox: [4]float64{0, yLo, 1, yHi}, DPI: 100})
	if err != nil {
		t.Fatalf("full Zoom: %v", err)
	}
	narrow, err := Zoom(Options{PDFData: pdf, Page: 0, Bbox: [4]float64{0, yLo, 0.3, yHi}, DPI: 100})
	if err != nil {
		t.Fatalf("narrow Zoom: %v", err)
	}

	if len(full.Rects) != 1 {
		t.Fatalf("y-band should isolate exactly the heading rect, got %d rects", len(full.Rects))
	}
	fullText := strings.TrimSpace(full.Rects[0].Text)
	if fullText != "Chapter One: Introduction" {
		t.Fatalf("y-band should isolate the heading; got %q", fullText)
	}

	if len(narrow.Rects) != 1 {
		t.Fatalf("narrow bbox still overlaps the heading; expected 1 rect, got %d", len(narrow.Rects))
	}
	narrowText := strings.TrimSpace(narrow.Rects[0].Text)
	if narrowText == fullText {
		t.Errorf("narrow bbox should crop the heading at char level, got full text %q", narrowText)
	}
	if !strings.HasPrefix(fullText, narrowText) || narrowText == "" {
		t.Errorf("narrow text %q should be a non-empty prefix of full %q", narrowText, fullText)
	}

	fullRight := full.Rects[0].Bbox[2]
	narrowRight := narrow.Rects[0].Bbox[2]
	if narrowRight >= fullRight {
		t.Errorf("narrow right edge %.5f should be < full right edge %.5f", narrowRight, fullRight)
	}
	// Overlap (not center-in / fully-contained): the char straddling x=0.3
	// is preserved, so the rect right edge sits a hair past 0.3 — but not by
	// more than one char-width.
	if narrowRight < 0.3 {
		t.Errorf("narrow right edge %.5f should reach the bbox edge under overlap criterion", narrowRight)
	}
	if narrowRight > 0.35 {
		t.Errorf("narrow right edge %.5f overshoots bbox right (0.3) by more than one char-width", narrowRight)
	}
}

func TestZoomInvalidPage(t *testing.T) {
	pdf := loadSamplePDF(t)
	_, err := Zoom(Options{PDFData: pdf, Page: 9999, Bbox: [4]float64{0, 0, 1, 1}, DPI: 100})
	if err == nil {
		t.Error("expected error for out-of-range page, got nil")
	}
}

func joinRectText(rects []Rect) string {
	parts := make([]string, len(rects))
	for i, r := range rects {
		parts[i] = r.Text
	}
	return strings.Join(parts, " ")
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
