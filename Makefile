# aibox build and check targets.
#
# Installing from a source checkout (needs Go >= 1.25; the go tool downloads
# a newer toolchain itself if yours is older):
#
#   make            # -> ./aibox
#   make install    # -> ~/.local/bin/aibox   (PREFIX=/usr/local for system-wide)
#
# `make check` is what CI runs.

GO      ?= go
BINARY  := aibox
PREFIX  ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS  = -s -w -X github.com/scuq/aibox/internal/app.Version=$(VERSION)

.PHONY: all build install test vet fmt-check staticcheck shellcheck check clean

all: build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/aibox

install: build
	install -d $(PREFIX)/bin
	install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	@echo "installed $(PREFIX)/bin/$(BINARY)"
	@case ":$$PATH:" in *":$(PREFIX)/bin:"*) ;; \
	    *) echo "note: $(PREFIX)/bin is not on your PATH" ;; esac

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

staticcheck:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...

# The embedded shell (entrypoint, git shim) still gets shellcheck — it runs
# inside every container aibox starts.
shellcheck:
	shellcheck -S warning assets/entrypoint.sh assets/git-shim.sh \
	    assets/ainotes/generate-image-notes.sh assets/ainotes/ainotes

check: fmt-check vet test

clean:
	rm -f $(BINARY)
