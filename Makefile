TAILWIND_VERSION := v4.3.3
TAILWIND := ./tailwindcss
CSS_IN  := web/css/input.css
CSS_OUT := web/static/css/app.css

# Detect the right standalone Tailwind build for this machine.
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)
ifeq ($(UNAME_S),Darwin)
  ifeq ($(UNAME_M),arm64)
    TAILWIND_ASSET := tailwindcss-macos-arm64
  else
    TAILWIND_ASSET := tailwindcss-macos-x64
  endif
else
  ifeq ($(UNAME_M),aarch64)
    TAILWIND_ASSET := tailwindcss-linux-arm64
  else
    TAILWIND_ASSET := tailwindcss-linux-x64
  endif
endif

.PHONY: all build css serve dev test e2e export lint clean tools

all: build

## build: compile the server and CLI into ./bin (CSS is embedded, so build it first)
build: css
	@mkdir -p bin
	go build -o bin/circe-server ./cmd/server
	go build -o bin/circe ./cmd/circe
	@echo "built bin/circe-server and bin/circe"

## css: compile Tailwind into the embedded stylesheet
css: $(TAILWIND)
	@$(TAILWIND) -i $(CSS_IN) -o $(CSS_OUT) --minify

## watch: rebuild CSS on change
watch: $(TAILWIND)
	$(TAILWIND) -i $(CSS_IN) -o $(CSS_OUT) --watch

$(TAILWIND):
	@echo "downloading tailwindcss $(TAILWIND_VERSION) ($(TAILWIND_ASSET))"
	@curl -sL -o $(TAILWIND) \
		https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/$(TAILWIND_ASSET)
	@chmod +x $(TAILWIND)

## serve: run the registry on :8080
serve: css
	go run ./cmd/server

## dev: run with templates and assets reloaded from disk
dev: css
	go run ./cmd/server -dev

## test: run the full test suite with the race detector
test:
	go test ./... -race

## e2e: drive the real site in a browser (needs Playwright)
e2e:
	python3 e2e/e2e.py

## export: write the registry out as plain files, to ./circe-export
export:
	go run ./cmd/server export

## lint: vet and check formatting
lint:
	go vet ./...
	@unformatted=$$(gofmt -l cmd internal web); \
	if [ -n "$$unformatted" ]; then echo "needs gofmt:"; echo "$$unformatted"; exit 1; fi

clean:
	rm -rf bin
	rm -f $(CSS_OUT)
