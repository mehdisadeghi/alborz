GO ?= go
NODE ?= node
ADDR ?= localhost:1323
# Served mail domains, bare for SRV discovery or with explicit upstreams:
# ARGS = example.org example.com=imaps://mail.example.com example.com=smtps://mail.example.com
ARGS ?=

.PHONY: build run watch fmt lint vuln test test-js login-key

# Go's cache and temp files go where these name, e.g. inside the checkout
# for a sandboxed agent; unset, Go's own defaults apply.
export GOCACHE GOTMPDIR TMPDIR
PACKAGES ?= ./...
TESTDIR ?= .
TESTFLAGS ?=
.PHONY: test-local vet-local security-local
test-local:
	$(GO) -C $(TESTDIR) test $(TESTFLAGS) $(PACKAGES)
vet-local:
	$(GO) vet $(PACKAGES)
security-local: vuln

build:
	$(GO) build -o alborz ./cmd/alborz

run: build
	./alborz -debug -addr $(ADDR) $(ARGS)

# Rebuild and restart on Go changes; reload templates in the running
# server (SIGUSR1, keeps sessions) on template changes.
watch:
	find templates -name '*.html' | entr -n pkill -USR1 -x alborz & \
	find . -name '*.go' | entr -nr $(MAKE) run

fmt:
	gofmt -w $$(git ls-files '*.go')

# The same diagnostics the editor shows: gofmt, vet, and gopls with its
# analyzers, which is where the modernize hints come from; then
# golangci-lint (staticcheck included) and revive (go test checks ADR 66's import rule);
# eslint and stylelint for the browser side. Go tools are pinned in
# tools/tools.mod, gopls and the others here; all are fetched on first use.
TOOL ?= $(GO) tool -modfile=tools/tools.mod
GOPLS ?= $(GO) run golang.org/x/tools/gopls@v0.24.0
GOLANGCI ?= $(TOOL) golangci-lint
REVIVE ?= $(TOOL) revive
ESLINT ?= npx --yes eslint@10.13.0
STYLELINT ?= npx --yes stylelint@17.15.0
.PHONY: lint-go lint-web
lint: vuln lint-go lint-web
lint-web:
	$(ESLINT) .
	$(STYLELINT) "templates/assets/**/*.css" "addons/*/web/**/*.css"
lint-go:
	@out=$$(gofmt -l $$(git ls-files '*.go')) && test -z "$$out" || { echo "$$out"; exit 1; }
	$(GO) vet ./...
	@out=$$($(GOPLS) check -severity=hint $$(git ls-files '*.go') 2>&1) && test -z "$$out" || { echo "$$out"; exit 1; }
	$(GOLANGCI) run ./...
	$(REVIVE) -config revive.toml -set_exit_status -exclude ./local/... ./...

# Report module advisories too; fail on vulnerable symbols reachable by our code.
vuln:
	$(TOOL) govulncheck -scan=symbol -show=verbose ./...

# Templates, locales, and every page a signed-in reader can open, served
# by an in-process IMAP server. No rig or credentials.
test: vuln test-js
	$(GO) test ./...

# Execute browser handlers against deterministic DOM fixtures; no npm dependencies.
test-js:
	$(NODE) --test tests/*.test.cjs

# Generate a key for -login-key.
login-key:
	openssl rand -base64 32

# Screenshots of fixture pages in headless Chrome, e.g.
# make shot ROUTE=calendar,settings WIDTH=1280,390 OUT=/tmp/shots
ROUTE ?= mail
WIDTH ?= 1280,390
HEIGHT ?= 900
.PHONY: shot
shot:
	$(GO) test -tags preview -run TestPreview -count=1 -v ./tools/preview -args -route "$(ROUTE)" -width "$(WIDTH)" -height "$(HEIGHT)" $(if $(OUT),-out "$(OUT)") $(if $(JS),-js "$(JS)") $(if $(CHROME),-chrome "$(CHROME)")

include contrib/packaging/desktop/desktop.mk
