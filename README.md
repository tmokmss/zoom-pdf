# pdf-zoom

A small CLI that renders a rectangular region of a PDF page at high DPI and emits
the text-layer characters that fall inside that region. Designed as a tool for
VLM-based PDF reading: pair the cropped image with the structured text JSON so
the model can cross-check what it visually sees against what the PDF encodes.

Built on [go-pdfium](https://github.com/klippa-app/go-pdfium) in **WebAssembly
mode** — no CGO, no dynamic libraries, single static binary.

## Build & test

Use the Makefile:

```bash
make build          # native binary       -> bin/pdf-zoom
make build-linux    # linux/amd64         -> bin/pdf-zoom-linux-amd64
make build-all      # both
make test           # integration tests against sample.pdf
make clean
```

Or directly:

```bash
go build -ldflags="-s -w" -o bin/pdf-zoom ./cmd/pdf-zoom

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
  -ldflags="-s -w" \
  -o bin/pdf-zoom-linux-amd64 \
  ./cmd/pdf-zoom
```

The `pdfium.wasm` binary and the wazero runtime are embedded into the resulting
Go binary — no external assets to ship. The Linux binary is statically linked
(no libc dependency).

`make test` drives the CLI end-to-end against `sample.pdf` with three success
cases and three error cases, writing artifacts into `testout/`.

## Usage

```
pdf-zoom --page N [--bbox x0,y0,x1,y1] [flags] <pdf-path>
```

| Flag             | Default       | Description                                                                |
| ---------------- | ------------- | -------------------------------------------------------------------------- |
| `--page`         | (required)    | 0-indexed page number.                                                     |
| `--bbox`         | `0,0,1,1`     | Region as `x0,y0,x1,y1` normalized to page (bottom-left origin), `x0<x1` and `y0<y1`. Default = full page. |
| `--dpi`          | `300`         | Render DPI for the cropped image.                                          |
| `--output-image` | `./zoom.png`  | PNG output path.                                                           |
| `--output-text`  | `-` (stdout)  | Text JSON output path; `-` means stdout.                                   |

Examples:

```bash
# whole page
./bin/pdf-zoom --page 0 sample.pdf

# top half
./bin/pdf-zoom --page 0 --bbox 0,0.5,1,1 sample.pdf

# center quarter
./bin/pdf-zoom --page 0 --bbox 0.25,0.25,0.75,0.75 --dpi 200 \
  --output-image /tmp/zoom.png \
  --output-text /tmp/zoom.json \
  sample.pdf
```

## Coordinate conventions

All bboxes — input `--bbox`, output `bbox_pdf`, output `bbox_image` — are
**normalized to [0,1]**.

- **`bbox_pdf`**: PDF page space, **bottom-left origin**. `(0,0)` = bottom-left of page, `(1,1)` = top-right.
- **`bbox_image`**: image space of the cropped output, **top-left origin**, relative to the crop. `(0,0)` = top-left of crop, `(1,1)` = bottom-right. Values may fall slightly outside `[0,1]` when a char/rect straddles the crop boundary.

To convert to pixels / points, multiply by the `image_size` / `page_size_pt`
returned in the JSON.

## Output JSON schema

```jsonc
{
  "pdf_path": "sample.pdf",
  "page": 0,
  "bbox_pdf":     [0.25, 0.25, 0.75, 0.75],   // requested bbox, clipped to page
  "dpi": 200,
  "page_size_pt": { "width": 595.0, "height": 842.0 },
  "image_size":   { "width": 833, "height": 833 },
  "page_rotation": 0,                         // 0 / 90 / 180 / 270
  "has_text_layer": true,                     // false ⇒ scanned PDF, use image only
  "rects": [                                  // PDFium-grouped text runs (≈ line fragments)
    {
      "text": "Hello world",
      "bbox_pdf":   [0.18, 0.78, 0.42, 0.81],
      "bbox_image": [0.31, 0.27, 0.79, 0.32]
    }
  ],
  "chars": [                                  // per-character details
    {
      "text": "あ",
      "bbox_pdf":   [0.176, 0.792, 0.195, 0.810],
      "bbox_image": [0.312, 0.243, 0.350, 0.281],
      "font_name": "MS-Mincho",               // optional; empty if unavailable
      "font_size": 10.5,                      // points; 0 if unavailable
      "is_vertical": false                    // heuristic from per-char angle
    }
  ]
}
```

`rects` (coarse) is what PDFium emits via `FPDFText_GetRect` — roughly one
entry per contiguous line/run. Best for VLM consumption: short, readable text
fragments with a single bbox each.

`chars` (fine) is the per-character view from `FPDFText_GetCharBox` — useful
for debugging, font detection, or building your own grouping.

For scanned PDFs (no text layer) you get `has_text_layer: false` and empty
`rects` / `chars`; consume the image only.

## Project layout

```
pdf-zoom-cli/
├── cmd/pdf-zoom/main.go    # CLI entry, flag parsing, JSON output
├── internal/pdfx/pdfx.go   # go-pdfium wrapper: render + char extraction
├── go.mod
└── README.md
```

## Known limitations

- **Page rotation**: the rendered image follows the page's natural rotation
  (so text appears upright), but the input `--bbox` is interpreted in the
  unrotated PDF coordinate space. On rotated pages the crop won't visually
  align with the bbox until rotation-aware coords are implemented.
- **`is_vertical`** is a heuristic on PDFium's `FPDFText_GetCharAngle` (treats
  `|angle| > ~30°` as vertical). Most horizontal CJK and Latin text reports
  `false`; vertical writing reports `true`.
- Font name/size/angle are best-effort per char; if PDFium can't determine
  them, the fields are left empty rather than failing the whole extraction.
