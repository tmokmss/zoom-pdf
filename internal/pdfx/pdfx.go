// Package pdfx wraps go-pdfium (webassembly mode) with the operations needed
// for the pdf-zoom CLI: render a page region to a PNG-ready image and extract
// the text-layer content (per-char and per-rect) that falls inside that region.
//
// Coordinate conventions (all bboxes are normalized [0,1] floats):
//   - bbox_pdf:   PDF page space, bottom-left origin, normalized to page size.
//                 (0,0) = bottom-left of page; (1,1) = top-right of page.
//   - bbox_image: image space of the cropped output, top-left origin, normalized
//                 to crop size. (0,0) = top-left of crop; (1,1) = bottom-right.
//                 Values may fall outside [0,1] when a char/rect straddles the
//                 crop boundary.
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

// Char is one character from the PDF text layer that overlaps the input bbox.
// All coordinates are normalized [0,1].
type Char struct {
	Text       string
	BboxPDF    [4]float64 // left, bottom, right, top — normalized to page
	BboxImage  [4]float64 // left, top, right, bottom — normalized to crop
	FontName   string
	FontSize   float64 // points
	IsVertical bool
}

// Rect is one of PDFium's natural text-rectangle groupings (roughly: a
// contiguous run of chars on the same line). All coordinates are normalized [0,1].
type Rect struct {
	Text      string
	BboxPDF   [4]float64
	BboxImage [4]float64
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
	Image          *image.RGBA // cropped image, top-left origin
	PageWidthPt    float64     // page width in points
	PageHeightPt   float64     // page height in points
	PageRotation   int         // 0, 90, 180, or 270
	HasTextLayer   bool
	Chars          []Char
	Rects          []Rect
	BboxClipped    [4]float64 // normalized bbox after clipping to page
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

	cropW := float64(cropRect.Dx())
	cropH := float64(cropRect.Dy())

	chars, hasText, err := extractCharsInBbox(inst, pageRef, bboxPt, pageW, pageH, scale, cropRect.Min.X, cropRect.Min.Y, cropW, cropH)
	if err != nil {
		return nil, fmt.Errorf("extract chars: %w", err)
	}

	rects, err := extractRectsInBbox(inst, pageRef, bboxPt, pageW, pageH, scale, cropRect.Min.X, cropRect.Min.Y, cropW, cropH)
	if err != nil {
		return nil, fmt.Errorf("extract rects: %w", err)
	}

	return &Result{
		Image:        cropped,
		PageWidthPt:  pageW,
		PageHeightPt: pageH,
		PageRotation: int(rot.PageRotation) * 90,
		HasTextLayer: hasText,
		Chars:        chars,
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

// pdfBoxToNormImage converts a points-space PDF bbox (left,bottom,right,top)
// to a normalized image-space bbox (left,top,right,bottom in [0,1] of crop).
func pdfBoxToNormImage(left, bottom, right, top, pageH, scale float64, cropOriginX, cropOriginY int, cropW, cropH float64) [4]float64 {
	imgLeft := left*scale - float64(cropOriginX)
	imgRight := right*scale - float64(cropOriginX)
	imgTop := (pageH-top)*scale - float64(cropOriginY)
	imgBottom := (pageH-bottom)*scale - float64(cropOriginY)
	return [4]float64{imgLeft / cropW, imgTop / cropH, imgRight / cropW, imgBottom / cropH}
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

func extractCharsInBbox(
	inst pdfium.Pdfium,
	pageRef requests.Page,
	bboxPt [4]float64,
	pageW, pageH, scale float64,
	cropOriginX, cropOriginY int,
	cropW, cropH float64,
) ([]Char, bool, error) {
	tp, err := inst.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: pageRef})
	if err != nil {
		return nil, false, fmt.Errorf("load text page: %w", err)
	}
	defer inst.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage})

	cc, err := inst.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: tp.TextPage})
	if err != nil {
		return nil, false, fmt.Errorf("count chars: %w", err)
	}
	if cc.Count <= 0 {
		return nil, false, nil
	}

	var chars []Char
	for i := 0; i < cc.Count; i++ {
		box, err := inst.FPDFText_GetCharBox(&requests.FPDFText_GetCharBox{TextPage: tp.TextPage, Index: i})
		if err != nil {
			return nil, true, fmt.Errorf("get char box at %d: %w", i, err)
		}
		left, right := math.Min(box.Left, box.Right), math.Max(box.Left, box.Right)
		bottom, top := math.Min(box.Bottom, box.Top), math.Max(box.Bottom, box.Top)
		if right < bboxPt[0] || left > bboxPt[2] || top < bboxPt[1] || bottom > bboxPt[3] {
			continue
		}

		uni, err := inst.FPDFText_GetUnicode(&requests.FPDFText_GetUnicode{TextPage: tp.TextPage, Index: i})
		if err != nil {
			return nil, true, fmt.Errorf("get unicode at %d: %w", i, err)
		}
		r := rune(uni.Unicode)
		if r == 0 {
			continue
		}

		var fontSize float64
		if s, err := inst.FPDFText_GetFontSize(&requests.FPDFText_GetFontSize{TextPage: tp.TextPage, Index: i}); err == nil {
			fontSize = s.FontSize
		}
		var fontName string
		if f, err := inst.FPDFText_GetFontInfo(&requests.FPDFText_GetFontInfo{TextPage: tp.TextPage, Index: i}); err == nil {
			fontName = f.FontName
		}
		var charAngle float32
		if a, err := inst.FPDFText_GetCharAngle(&requests.FPDFText_GetCharAngle{TextPage: tp.TextPage, Index: i}); err == nil {
			charAngle = a.CharAngle
		}

		chars = append(chars, Char{
			Text:       string(r),
			BboxPDF:    pointsToNorm([4]float64{left, bottom, right, top}, pageW, pageH),
			BboxImage:  pdfBoxToNormImage(left, bottom, right, top, pageH, scale, cropOriginX, cropOriginY, cropW, cropH),
			FontName:   fontName,
			FontSize:   fontSize,
			IsVertical: math.Abs(float64(charAngle)) > 0.5,
		})
	}
	return chars, true, nil
}

// extractRectsInBbox uses PDFium's FPDFText_CountRects/GetRect to get coarse
// text groupings (roughly one rect per contiguous line/run), filters by overlap
// with the input bbox, and reads each rect's text via FPDFText_GetBoundedText.
func extractRectsInBbox(
	inst pdfium.Pdfium,
	pageRef requests.Page,
	bboxPt [4]float64,
	pageW, pageH, scale float64,
	cropOriginX, cropOriginY int,
	cropW, cropH float64,
) ([]Rect, error) {
	tp, err := inst.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: pageRef})
	if err != nil {
		return nil, fmt.Errorf("load text page: %w", err)
	}
	defer inst.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage})

	cr, err := inst.FPDFText_CountRects(&requests.FPDFText_CountRects{
		TextPage: tp.TextPage, StartIndex: 0, Count: -1,
	})
	if err != nil {
		return nil, fmt.Errorf("count rects: %w", err)
	}
	if cr.Count <= 0 {
		return nil, nil
	}

	var rects []Rect
	for i := 0; i < cr.Count; i++ {
		rr, err := inst.FPDFText_GetRect(&requests.FPDFText_GetRect{
			TextPage: tp.TextPage, Index: i,
		})
		if err != nil {
			return nil, fmt.Errorf("get rect %d: %w", i, err)
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
			return nil, fmt.Errorf("get bounded text %d: %w", i, err)
		}

		rects = append(rects, Rect{
			Text:      bt.Text,
			BboxPDF:   pointsToNorm([4]float64{left, bottom, right, top}, pageW, pageH),
			BboxImage: pdfBoxToNormImage(left, bottom, right, top, pageH, scale, cropOriginX, cropOriginY, cropW, cropH),
		})
	}
	return rects, nil
}
