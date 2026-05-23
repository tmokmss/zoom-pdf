# pdf-zoom build & test targets.
#
#   make build         # native binary -> bin/pdf-zoom
#   make build-linux   # linux/amd64    -> bin/pdf-zoom-linux-amd64
#   make build-all     # both of the above
#   make test          # run the binary against sample.pdf with a few bboxes
#   make clean         # remove bin/ and test outputs

BIN_DIR    := bin
BIN        := $(BIN_DIR)/pdf-zoom
BIN_LINUX  := $(BIN_DIR)/pdf-zoom-linux-amd64

LDFLAGS    := -s -w
PKG        := ./cmd/pdf-zoom

SAMPLE_PDF := sample.pdf
TEST_DIR   := testout

.PHONY: build build-linux build-all test test-unit test-integration test-cli clean help

help:
	@awk -F':.*##' '/^[a-zA-Z_-]+:.*##/ {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build native binary
	@mkdir -p $(BIN_DIR)
	go build -ldflags="$(LDFLAGS)" -o $(BIN) $(PKG)
	@echo "built $(BIN) ($$(du -h $(BIN) | cut -f1))"

build-linux: ## Cross-compile linux/amd64 binary (CGO disabled)
	@mkdir -p $(BIN_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
	  go build -ldflags="$(LDFLAGS)" -o $(BIN_LINUX) $(PKG)
	@echo "built $(BIN_LINUX) ($$(du -h $(BIN_LINUX) | cut -f1))"

build-all: build build-linux ## Build native + linux/amd64

test: test-unit test-integration test-cli ## Run all tests (unit + integration + CLI smoke)

test-unit: ## Fast unit tests (coord helpers, parseBbox)
	go test ./...

test-integration: ## go test with -tags=integration (drives pdfium against sample.pdf)
	@test -f $(SAMPLE_PDF) || (echo "missing $(SAMPLE_PDF)"; exit 1)
	go test -tags=integration ./internal/pdfx/...

# CLI smoke tests: drive the CLI end-to-end against sample.pdf.
# Each case writes its PNG + JSON under $(TEST_DIR)/ and prints a 1-line summary.
test-cli: build ## End-to-end smoke test of the CLI binary against sample.pdf
	@test -f $(SAMPLE_PDF) || (echo "missing $(SAMPLE_PDF)"; exit 1)
	@rm -rf $(TEST_DIR)
	@mkdir -p $(TEST_DIR)
	@echo "==> case 1: whole page, default dpi"
	./$(BIN) --page 0 --bbox 0,0,1,1 \
	  --output-image $(TEST_DIR)/full.png \
	  --output-text $(TEST_DIR)/full.json \
	  $(SAMPLE_PDF)
	@echo "==> case 2: top half, low dpi"
	./$(BIN) --page 0 --bbox 0,0.5,1,1 --dpi 150 \
	  --output-image $(TEST_DIR)/top.png \
	  --output-text $(TEST_DIR)/top.json \
	  $(SAMPLE_PDF)
	@echo "==> case 3: center quarter, stdout JSON"
	./$(BIN) --page 0 --bbox 0.25,0.25,0.75,0.75 --dpi 100 \
	  --output-image $(TEST_DIR)/center.png \
	  $(SAMPLE_PDF) > $(TEST_DIR)/center.json
	@echo "==> error cases (expected non-zero exit, captured)"
	@! ./$(BIN) 2>$(TEST_DIR)/err_noargs.txt || (echo "expected failure"; exit 1)
	@! ./$(BIN) --page 99 --bbox 0,0,1,1 $(SAMPLE_PDF) 2>$(TEST_DIR)/err_page.txt || (echo "expected failure"; exit 1)
	@! ./$(BIN) --page 0 --bbox 1,2,3 $(SAMPLE_PDF) 2>$(TEST_DIR)/err_bbox.txt || (echo "expected failure"; exit 1)
	@! ./$(BIN) --page 0 --bbox 0,0,2,1 $(SAMPLE_PDF) 2>$(TEST_DIR)/err_outofrange.txt || (echo "expected failure"; exit 1)
	@echo "==> summary"
	@for f in $(TEST_DIR)/*.png; do \
	  printf "  %-30s %s\n" "$$f" "$$(du -h $$f | cut -f1)"; \
	done
	@for f in $(TEST_DIR)/*.json; do \
	  python3 -c "import json,sys; d=json.load(open('$$f')); print(f'  {sys.argv[1]:<30} rects={len(d[\"rects\"])}')" "$$f"; \
	done
	@echo "OK"

clean: ## Remove built binaries and test outputs
	rm -rf $(BIN_DIR) $(TEST_DIR)
