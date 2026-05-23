# zoom-pdf

A small CLI that renders a rectangular region of a PDF page at high DPI and emits
the text-layer rects (≈ line-level fragments) that fall inside that region.
Designed as a tool for VLM-based PDF reading: pair the cropped image with the
structured text JSON so the model can cross-check what it visually sees against
what the PDF encodes.

Built on [go-pdfium](https://github.com/klippa-app/go-pdfium) in **WebAssembly
mode** — no CGO, no dynamic libraries, single static binary.

## Build & test

Use the Makefile:

```bash
make build          # native binary       -> bin/zoom-pdf
make build-linux    # linux/amd64         -> bin/zoom-pdf-linux-amd64
make build-all      # both
make test           # integration tests against testdata/sample.pdf
make clean
```

Or directly:

```bash
go build -ldflags="-s -w" -o bin/zoom-pdf ./cmd/zoom-pdf

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
  -ldflags="-s -w" \
  -o bin/zoom-pdf-linux-amd64 \
  ./cmd/zoom-pdf
```

The `pdfium.wasm` binary and the wazero runtime are embedded into the resulting
Go binary — no external assets to ship. The Linux binary is statically linked
(no libc dependency).

`make test` drives the CLI end-to-end against `testdata/sample.pdf` (a small,
born-digital PDF with placeholder Lorem-ipsum text) with three success cases
and three error cases, writing artifacts into `testout/`.

## Usage

```
zoom-pdf --page N [--bbox x0,y0,x1,y1] [flags] <pdf-path>
```

| Flag             | Default       | Description                                                                |
| ---------------- | ------------- | -------------------------------------------------------------------------- |
| `--page`         | (required)    | 0-indexed page number.                                                     |
| `--bbox`         | `0,0,1,1`     | Region as `x0,y0,x1,y1` normalized to page (bottom-left origin), `x0<x1` and `y0<y1`. Default = full page. |
| `--dpi`          | `300`         | Render DPI for the cropped image.                                          |
| `--output-image` | `./zoom.png`  | PNG output path.                                                           |
| `--output-text`  | `-` (stdout)  | Text output path; `-` means stdout.                                        |
| `--plain`        | `false`       | Emit text output as a compact plain format instead of JSON (see below).    |

Examples:

```bash
# whole page
./bin/zoom-pdf --page 0 sample.pdf

# top half
./bin/zoom-pdf --page 0 --bbox 0,0.5,1,1 sample.pdf

# center quarter
./bin/zoom-pdf --page 0 --bbox 0.25,0.25,0.75,0.75 --dpi 200 \
  --output-image /tmp/zoom.png \
  --output-text /tmp/zoom.json \
  sample.pdf
```

## Coordinate conventions

All bboxes — input `--bbox` and output `bbox` — are **normalized to [0,1]** in
**PDF page space**, **bottom-left origin**. `(0,0)` = bottom-left of page,
`(1,1)` = top-right.

To convert to points, multiply by the `page_size_pt` returned in the JSON.

## Output JSON schema

```jsonc
{
  "pdf_path": "sample.pdf",
  "page": 0,
  "bbox":         [0.25, 0.25, 0.75, 0.75],   // requested bbox, clipped to page
  "dpi": 200,
  "page_size_pt": { "width": 595.0, "height": 842.0 },
  "image_size":   { "width": 833, "height": 833 },
  "page_rotation": 0,                         // 0 / 90 / 180 / 270
  "has_text_layer": true,                     // false ⇒ scanned PDF, use image only
  "rects": [                                  // PDFium-grouped text runs (≈ line fragments)
    {
      "text": "Hello world",
      "bbox": [0.18, 0.78, 0.42, 0.81]
    }
  ]
}
```

`rects` is what PDFium emits via `FPDFText_GetRect` — roughly one entry per
contiguous line/run. Each entry's `text` is the joined string and `bbox` is the
enclosing rectangle in normalized page coordinates.

For scanned PDFs (no text layer) you get `has_text_layer: false` and an empty
`rects` array; consume the image only.

## Plain output format (`--plain`)

`--plain` emits the same information as the JSON above, but in a compact
line-oriented form designed for low token cost when feeding text into an LLM.

```
pdf=<path> page=<n> bbox=<x0,y0,x1,y1> dpi=<n> page_pt=<W>x<H> image_px=<W>x<H> rotation=<0|90|180|270> text_layer=<true|false>
<x0,y0,x1,y1>\t<text>
<x0,y0,x1,y1>\t<text>
...
```

- Line 1 is the header: space-separated `key=value` pairs, one per JSON
  metadata field.
- Each subsequent line is one rect: bbox and text separated by a single tab.
- Bbox values are the same 5-significant-digit-rounded normalized coordinates
  used in JSON output.
- Tabs / newlines inside `text` are replaced with a single space so each rect
  stays on exactly one line.
- For scanned PDFs (no text layer) only the header line is emitted.

## Project layout

```
zoom-pdf/
├── cmd/zoom-pdf/main.go    # CLI entry, flag parsing, JSON output
├── internal/pdfx/pdfx.go   # go-pdfium wrapper: render + rect extraction
├── go.mod
└── README.md
```

## Known limitations

- **Page rotation**: the rendered image follows the page's natural rotation
  (so text appears upright), but the input `--bbox` is interpreted in the
  unrotated PDF coordinate space. On rotated pages the crop won't visually
  align with the bbox until rotation-aware coords are implemented.
