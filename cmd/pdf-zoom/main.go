// pdf-zoom renders a rectangular region of a PDF page at high DPI and emits
// the text-layer content (per-char + per-rect) that falls inside that region.
// The image is written as PNG; the text data is emitted as JSON.
//
// All bbox coordinates (input and output) are normalized [0,1] in PDF page
// space (bottom-left origin). See README.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/tmokmss/pdf-zoom-cli/internal/pdfx"
)

type imageSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type pageSizePt struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type charJSON struct {
	Text       string     `json:"text"`
	Bbox       [4]float64 `json:"bbox"`
	FontName   string     `json:"font_name,omitempty"`
	FontSize   float64    `json:"font_size,omitempty"`
	IsVertical bool       `json:"is_vertical"`
}

type rectJSON struct {
	Text string     `json:"text"`
	Bbox [4]float64 `json:"bbox"`
}

type output struct {
	PDFPath      string     `json:"pdf_path"`
	Page         int        `json:"page"`
	Bbox         [4]float64 `json:"bbox"`
	DPI          int        `json:"dpi"`
	PageSizePt   pageSizePt `json:"page_size_pt"`
	ImageSize    imageSize  `json:"image_size"`
	PageRotation int        `json:"page_rotation"`
	HasTextLayer bool       `json:"has_text_layer"`
	Rects        []rectJSON `json:"rects"`
	Chars        []charJSON `json:"chars"`
}

func main() {
	var (
		page     = flag.Int("page", -1, "Page number (0-indexed). Required.")
		bboxFlag = flag.String("bbox", "0,0,1,1", "Region as x0,y0,x1,y1 normalized to page (bottom-left origin). Default = full page.")
		dpi      = flag.Int("dpi", 300, "Render DPI.")
		imageOut = flag.String("output-image", "./zoom.png", "Output image path.")
		textOut  = flag.String("output-text", "-", "Text JSON output path, or '-' for stdout.")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: pdf-zoom --page N [--bbox x0,y0,x1,y1] [flags] <pdf-path>\n\n")
		fmt.Fprintf(os.Stderr, "bbox values are normalized [0,1] in PDF page space (bottom-left origin).\n")
		fmt.Fprintf(os.Stderr, "e.g. --bbox 0,0.5,1,1 = top half of the page.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		die("expected exactly one positional argument: <pdf-path>")
	}
	if *page < 0 {
		die("--page is required and must be >= 0")
	}

	bbox, err := parseBbox(*bboxFlag)
	if err != nil {
		die(err.Error())
	}

	pdfPath := flag.Arg(0)
	pdfData, err := os.ReadFile(pdfPath)
	if err != nil {
		die(fmt.Sprintf("read PDF: %v", err))
	}

	res, err := pdfx.Zoom(pdfx.Options{
		PDFData: pdfData,
		Page:    *page,
		Bbox:    bbox,
		DPI:     *dpi,
	})
	if err != nil {
		die(err.Error())
	}

	if err := writePNG(*imageOut, res); err != nil {
		die(fmt.Sprintf("write image: %v", err))
	}

	if err := writeJSON(*textOut, buildOutput(pdfPath, *page, *dpi, res)); err != nil {
		die(fmt.Sprintf("write text JSON: %v", err))
	}
}

func parseBbox(s string) ([4]float64, error) {
	var bbox [4]float64
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return bbox, fmt.Errorf("bbox must have 4 comma-separated numbers, got %q", s)
	}
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return bbox, fmt.Errorf("bbox component %d (%q): %v", i, p, err)
		}
		if v < 0 || v > 1 {
			return bbox, fmt.Errorf("bbox component %d (%v) outside [0,1]; bboxes are normalized to page size", i, v)
		}
		bbox[i] = v
	}
	return bbox, nil
}

func writePNG(path string, res *pdfx.Result) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, res.Image)
}

func writeJSON(target string, payload output) error {
	var w io.Writer = os.Stdout
	if target != "-" {
		f, err := os.Create(target)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(payload)
}

func buildOutput(pdfPath string, page, dpi int, res *pdfx.Result) output {
	chars := make([]charJSON, len(res.Chars))
	for i, c := range res.Chars {
		chars[i] = charJSON{
			Text: c.Text, Bbox: roundBbox(c.Bbox),
			FontName: c.FontName, FontSize: c.FontSize, IsVertical: c.IsVertical,
		}
	}
	rects := make([]rectJSON, len(res.Rects))
	for i, r := range res.Rects {
		rects[i] = rectJSON{Text: r.Text, Bbox: roundBbox(r.Bbox)}
	}
	b := res.Image.Bounds()
	return output{
		PDFPath:      pdfPath,
		Page:         page,
		Bbox:         roundBbox(res.BboxClipped),
		DPI:          dpi,
		PageSizePt:   pageSizePt{Width: res.PageWidthPt, Height: res.PageHeightPt},
		ImageSize:    imageSize{Width: b.Dx(), Height: b.Dy()},
		PageRotation: res.PageRotation,
		HasTextLayer: res.HasTextLayer,
		Rects:        rects,
		Chars:        chars,
	}
}

// roundBbox rounds each component to 5 significant digits to keep JSON output
// compact. Go's float-to-JSON encoder uses shortest-round-trip, so a value
// rounded to 5 sig digits typically encodes as ≤7 chars.
func roundBbox(b [4]float64) [4]float64 {
	var out [4]float64
	for i, v := range b {
		out[i] = roundSig(v, 5)
	}
	return out
}

func roundSig(v float64, sig int) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	mag := math.Floor(math.Log10(math.Abs(v)))
	factor := math.Pow(10, float64(sig-1)-mag)
	return math.Round(v*factor) / factor
}

func die(msg string) {
	fmt.Fprintln(os.Stderr, "pdf-zoom: "+msg)
	os.Exit(1)
}
