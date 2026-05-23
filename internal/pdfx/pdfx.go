// Package pdfx wraps go-pdfium (webassembly mode) with the operations needed
// for the zoom-pdf CLI: render a page region to a PNG-ready image and extract
// the text-layer rects that fall inside that region.
//
// Coordinate conventions (bbox is normalized [0,1] floats):
//   - bbox: PDF page space, bottom-left origin, normalized to page size.
//           (0,0) = bottom-left of page; (1,1) = top-right of page.
package pdfx

import (
	"fmt"
	"image"
	"io"
	"math"
	"strings"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// Rect is one of PDFium's natural text-rectangle groupings (roughly: a
// contiguous run of chars on the same line). All coordinates are normalized [0,1].
type Rect struct {
	Text string
	Bbox [4]float64
}

// Options configures Zoom. Bbox is normalized [0,1] in PDF page space
// (bottom-left origin); use {0,0,1,1} for the whole page.
type Options struct {
	PDFData []byte
	Page    int
	Bbox    [4]float64 // x0,y0,x1,y1 normalized to page; x0<x1 and y0<y1
	DPI     int
}

// Result is what Zoom produces.
type Result struct {
	Image        *image.RGBA // cropped image, top-left origin
	PageWidthPt  float64     // page width in points
	PageHeightPt float64     // page height in points
	PageRotation int         // 0, 90, 180, or 270
	HasTextLayer bool
	Rects        []Rect
	BboxClipped  [4]float64 // normalized bbox after clipping to page
}

// Zoom opens the PDF, renders the requested region at the requested DPI, and
// extracts the text-layer content that overlaps the region.
func Zoom(opts Options) (*Result, error) {
	if opts.DPI <= 0 {
		return nil, fmt.Errorf("dpi must be positive, got %d", opts.DPI)
	}
	if opts.Bbox[0] >= opts.Bbox[2] || opts.Bbox[1] >= opts.Bbox[3] {
		return nil, fmt.Errorf("bbox must satisfy x0<x1 and y0<y1, got %v", opts.Bbox)
	}

	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  1,
		MaxTotal: 1,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
	})
	if err != nil {
		return nil, fmt.Errorf("init pdfium pool: %w", err)
	}
	defer pool.Close()

	inst, err := pool.GetInstance(30 * time.Second)
	if err != nil {
		return nil, fmt.Errorf("get pdfium instance: %w", err)
	}
	defer inst.Close()

	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &opts.PDFData})
	if err != nil {
		return nil, fmt.Errorf("open document: %w", err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})

	pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, fmt.Errorf("get page count: %w", err)
	}
	if opts.Page < 0 || opts.Page >= pc.PageCount {
		return nil, fmt.Errorf("page %d out of range (document has %d page(s))", opts.Page, pc.PageCount)
	}

	pageRef := requests.Page{
		ByIndex: &requests.PageByIndex{Document: doc.Document, Index: opts.Page},
	}

	sz, err := inst.GetPageSize(&requests.GetPageSize{Page: pageRef})
	if err != nil {
		return nil, fmt.Errorf("get page size: %w", err)
	}
	pageW, pageH := sz.Width, sz.Height

	// Convert normalized bbox -> points, clip to page.
	bboxPt, err := clipBboxToPagePoints(normToPoints(opts.Bbox, pageW, pageH), pageW, pageH)
	if err != nil {
		return nil, err
	}

	rot, err := inst.FPDFPage_GetRotation(&requests.FPDFPage_GetRotation{Page: pageRef})
	if err != nil {
		return nil, fmt.Errorf("get page rotation: %w", err)
	}

	scale := float64(opts.DPI) / 72.0

	render, err := inst.RenderPageInDPI(&requests.RenderPageInDPI{
		Page: pageRef,
		DPI:  opts.DPI,
	})
	if err != nil {
		return nil, fmt.Errorf("render page: %w", err)
	}
	defer render.Cleanup()

	full := render.Result.Image
	if full == nil {
		return nil, fmt.Errorf("render produced no image")
	}

	cropRect := bboxPointsToImageRect(bboxPt, pageH, scale, full.Bounds())
	if cropRect.Empty() {
		return nil, fmt.Errorf("crop region is empty after clipping")
	}
	cropped := copyRegion(full, cropRect)

	rects, hasText, err := extractRectsInBbox(inst, pageRef, bboxPt, pageW, pageH)
	if err != nil {
		return nil, fmt.Errorf("extract rects: %w", err)
	}

	return &Result{
		Image:        cropped,
		PageWidthPt:  pageW,
		PageHeightPt: pageH,
		PageRotation: int(rot.PageRotation) * 90,
		HasTextLayer: hasText,
		Rects:        rects,
		BboxClipped:  pointsToNorm(bboxPt, pageW, pageH),
	}, nil
}

// ---- coord helpers ----

func normToPoints(b [4]float64, pageW, pageH float64) [4]float64 {
	return [4]float64{b[0] * pageW, b[1] * pageH, b[2] * pageW, b[3] * pageH}
}

func pointsToNorm(b [4]float64, pageW, pageH float64) [4]float64 {
	return [4]float64{b[0] / pageW, b[1] / pageH, b[2] / pageW, b[3] / pageH}
}

func clipBboxToPagePoints(b [4]float64, w, h float64) ([4]float64, error) {
	clipped := [4]float64{
		math.Max(0, b[0]),
		math.Max(0, b[1]),
		math.Min(w, b[2]),
		math.Min(h, b[3]),
	}
	if clipped[0] >= clipped[2] || clipped[1] >= clipped[3] {
		return clipped, fmt.Errorf("bbox %v lies outside page (%.1f x %.1f pt)", b, w, h)
	}
	return clipped, nil
}

func bboxPointsToImageRect(bbox [4]float64, pageH, scale float64, imgBounds image.Rectangle) image.Rectangle {
	x0 := int(math.Round(bbox[0] * scale))
	x1 := int(math.Round(bbox[2] * scale))
	yTop := int(math.Round((pageH - bbox[3]) * scale))
	yBot := int(math.Round((pageH - bbox[1]) * scale))
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if yBot <= yTop {
		yBot = yTop + 1
	}
	return image.Rect(x0, yTop, x1, yBot).Intersect(imgBounds)
}

func copyRegion(src *image.RGBA, r image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	rowBytes := r.Dx() * 4
	for y := 0; y < r.Dy(); y++ {
		srcOff := src.PixOffset(r.Min.X, r.Min.Y+y)
		dstOff := dst.PixOffset(0, y)
		copy(dst.Pix[dstOff:dstOff+rowBytes], src.Pix[srcOff:srcOff+rowBytes])
	}
	return dst
}

// ---- text extraction ----

// charInfo holds a single text-page character's position + unicode codepoint.
// All coordinates are PDF page points (bottom-left origin).
type charInfo struct {
	index   int
	left    float64
	right   float64
	bottom  float64
	top     float64
	unicode rune
}

// extractRectsInBbox returns the text-layer rects that overlap bboxPt.
//
// PDFium groups text into coarse rects (roughly one per contiguous line/run)
// via FPDFText_CountRects/GetRect; we keep that grouping so output structure
// matches PDFium's natural line layout. The catch is that a rect that merely
// clips the bbox brings in all of its chars, including ones well outside the
// region of interest. So we re-filter at the character level:
//
//  1. Enumerate every char on the page once via FPDFText_GetCharBox/GetUnicode.
//  2. Pre-build an "in-bbox index" of char indices whose box overlaps bboxPt.
//  3. For each rect that overlaps bboxPt, walk the in-bbox index and keep
//     chars that also overlap that rect's box. The output bbox is the tight
//     min/max of selected char boxes (not the original rect), and the text is
//     selected chars concatenated in char-index order (PDFium stream order).
//
// Rects with zero surviving chars are dropped. Overlap (not center-in or
// fully-contained) is the inclusion test, so any char that visibly clips into
// the region is preserved.
//
// The bool return is true when the PDF has a text layer at all (regardless of
// whether any rect overlaps the bbox), so the caller can distinguish a
// scanned PDF from a born-digital one with text outside the requested region.
func extractRectsInBbox(
	inst pdfium.Pdfium,
	pageRef requests.Page,
	bboxPt [4]float64,
	pageW, pageH float64,
) ([]Rect, bool, error) {
	tp, err := inst.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: pageRef})
	if err != nil {
		return nil, false, fmt.Errorf("load text page: %w", err)
	}
	defer inst.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage})

	cc, err := inst.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: tp.TextPage})
	if err != nil {
		return nil, false, fmt.Errorf("count chars: %w", err)
	}
	hasText := cc.Count > 0

	chars, inBbox, err := loadCharsAndIndex(inst, tp.TextPage, cc.Count, bboxPt)
	if err != nil {
		return nil, hasText, err
	}

	cr, err := inst.FPDFText_CountRects(&requests.FPDFText_CountRects{
		TextPage: tp.TextPage, StartIndex: 0, Count: -1,
	})
	if err != nil {
		return nil, hasText, fmt.Errorf("count rects: %w", err)
	}
	if cr.Count <= 0 {
		return nil, hasText, nil
	}

	var rects []Rect
	for i := 0; i < cr.Count; i++ {
		rr, err := inst.FPDFText_GetRect(&requests.FPDFText_GetRect{
			TextPage: tp.TextPage, Index: i,
		})
		if err != nil {
			return nil, hasText, fmt.Errorf("get rect %d: %w", i, err)
		}
		// PDFium returns Top/Bottom in PDF space (Top > Bottom).
		rectBox := [4]float64{
			math.Min(rr.Left, rr.Right),
			math.Min(rr.Top, rr.Bottom),
			math.Max(rr.Left, rr.Right),
			math.Max(rr.Top, rr.Bottom),
		}
		if !boxesOverlap(rectBox, bboxPt) {
			continue
		}

		text, tight, ok := charsInRect(chars, inBbox, rectBox)
		if !ok {
			continue
		}
		rects = append(rects, Rect{
			Text: text,
			Bbox: pointsToNorm(tight, pageW, pageH),
		})
	}
	return rects, hasText, nil
}

// loadCharsAndIndex pulls every char's box + unicode from PDFium and returns
// the full slice plus the indices of chars whose box overlaps bboxPt (the
// "bbox index" reused for each rect). One FPDFText_GetCharBox + one
// FPDFText_GetUnicode call per char — the dominant cost of extraction.
func loadCharsAndIndex(inst pdfium.Pdfium, tp references.FPDF_TEXTPAGE, count int, bboxPt [4]float64) ([]charInfo, []int, error) {
	chars := make([]charInfo, 0, count)
	var inBbox []int
	for i := 0; i < count; i++ {
		cb, err := inst.FPDFText_GetCharBox(&requests.FPDFText_GetCharBox{TextPage: tp, Index: i})
		if err != nil {
			return nil, nil, fmt.Errorf("get char box %d: %w", i, err)
		}
		uc, err := inst.FPDFText_GetUnicode(&requests.FPDFText_GetUnicode{TextPage: tp, Index: i})
		if err != nil {
			return nil, nil, fmt.Errorf("get unicode %d: %w", i, err)
		}
		c := charInfo{
			index:   i,
			left:    math.Min(cb.Left, cb.Right),
			right:   math.Max(cb.Left, cb.Right),
			bottom:  math.Min(cb.Top, cb.Bottom),
			top:     math.Max(cb.Top, cb.Bottom),
			unicode: rune(uc.Unicode),
		}
		chars = append(chars, c)
		if boxesOverlap([4]float64{c.left, c.bottom, c.right, c.top}, bboxPt) {
			inBbox = append(inBbox, i)
		}
	}
	return chars, inBbox, nil
}

// charsInRect picks chars from the in-bbox index whose box also overlaps
// rectBox, then returns the concatenated text (in char-index order) and the
// tight min/max bbox of the selected chars. ok=false means no chars matched.
func charsInRect(chars []charInfo, inBbox []int, rectBox [4]float64) (string, [4]float64, bool) {
	var b strings.Builder
	tight := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	any := false
	for _, idx := range inBbox {
		c := chars[idx]
		if !boxesOverlap([4]float64{c.left, c.bottom, c.right, c.top}, rectBox) {
			continue
		}
		// Unicode 0 means PDFium couldn't map the glyph; skip so we don't
		// emit U+0000 in the output, but still let it influence the tight box
		// if other selected chars exist on this rect.
		if c.unicode != 0 {
			b.WriteRune(c.unicode)
		}
		if c.left < tight[0] {
			tight[0] = c.left
		}
		if c.bottom < tight[1] {
			tight[1] = c.bottom
		}
		if c.right > tight[2] {
			tight[2] = c.right
		}
		if c.top > tight[3] {
			tight[3] = c.top
		}
		any = true
	}
	if !any || b.Len() == 0 {
		return "", tight, false
	}
	return b.String(), tight, true
}

// boxesOverlap reports whether two AABBs in {x0,y0,x1,y1} form share any area
// (touching edges count as overlap).
func boxesOverlap(a, b [4]float64) bool {
	return !(a[2] < b[0] || a[0] > b[2] || a[3] < b[1] || a[1] > b[3])
}
