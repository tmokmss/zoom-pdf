// Package pdfx wraps go-pdfium (webassembly mode) with the operations needed
// for the pdf-zoom CLI: render a page region to a PNG-ready image and extract
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
	"time"

	"github.com/klippa-app/go-pdfium"
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

// extractRectsInBbox uses PDFium's FPDFText_CountRects/GetRect to get coarse
// text groupings (roughly one rect per contiguous line/run), filters by overlap
// with the input bbox, and reads each rect's text via FPDFText_GetBoundedText.
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
		left, right := math.Min(rr.Left, rr.Right), math.Max(rr.Left, rr.Right)
		bottom, top := math.Min(rr.Top, rr.Bottom), math.Max(rr.Top, rr.Bottom)
		if right < bboxPt[0] || left > bboxPt[2] || top < bboxPt[1] || bottom > bboxPt[3] {
			continue
		}

		bt, err := inst.FPDFText_GetBoundedText(&requests.FPDFText_GetBoundedText{
			TextPage: tp.TextPage,
			Left:     left,
			Top:      top,
			Right:    right,
			Bottom:   bottom,
		})
		if err != nil {
			return nil, hasText, fmt.Errorf("get bounded text %d: %w", i, err)
		}

		rects = append(rects, Rect{
			Text: bt.Text,
			Bbox: pointsToNorm([4]float64{left, bottom, right, top}, pageW, pageH),
		})
	}
	return rects, hasText, nil
}
