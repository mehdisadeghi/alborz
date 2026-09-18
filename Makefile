GO ?= go
ADDR ?= localhost:1323
# Served mail domains, bare for SRV discovery or with explicit upstreams:
# ARGS = example.org example.com=imaps://mail.example.com example.com=smtps://mail.example.com
ARGS ?=

.PHONY: build run watch fmt lint test login-key

# What the build reads: the Go sources and everything embedded. The go
# tool marks a build dirty for any file git does not know, wherever it
# lies; this asks only about these. Take $(STAMP) off a build line and
# the go tool's own answer is back.
BUILD_INPUTS = '*.go' go.mod go.sum locales themes/alborz 'plugins/*/public/*'
MODIFIED = $(shell test -z "$$(git status --porcelain --untracked-files=all -- $(BUILD_INPUTS) 2>/dev/null)" && echo false || echo true)
# A build of a tagged commit carries the tag, as a release from CI does.
VERSION = $(shell git describe --tags --exact-match 2>/dev/null)
STAMP = -X main.modified=$(MODIFIED) $(if $(VERSION),-X main.version=$(VERSION))

build:
	$(GO) build -ldflags "$(STAMP)" -o alborz ./cmd/alborz

run: build
	./alborz -theme alborz -addr $(ADDR) $(ARGS)

# Rebuild and restart on Go changes; reload templates in the running
# server (SIGUSR1, keeps sessions) on theme changes.
watch:
	find themes plugins -name '*.html' | entr -n pkill -USR1 -x alborz & \
	find . -name '*.go' | entr -nr $(MAKE) run

fmt:
	gofmt -w .

# The same diagnostics the editor shows: gofmt, vet, and gopls with its
# analyzers, which is where the modernize hints come from.
GOPLS ?= gopls
lint:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	$(GO) vet ./...
	$(GOPLS) check $$(git ls-files '*.go')

# Templates, locales, and every page a signed-in reader can open, served
# by an in-process IMAP server. No network, no rig, no credentials.
test:
	$(GO) test ./...

# Generate a Fernet key for -login-key.
login-key:
	$(GO) run github.com/fernet/fernet-go/cmd/fernet-keygen
