GO ?= go
NODE ?= node
ADDR ?= localhost:1323
# Served mail domains, bare for SRV discovery or with explicit upstreams:
# ARGS = example.org example.com=imaps://mail.example.com example.com=smtps://mail.example.com
ARGS ?=

.PHONY: build run watch fmt lint vuln test test-js login-key

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
	gofmt -w .

# The same diagnostics the editor shows: gofmt, vet, and gopls with its
# analyzers, which is where the modernize hints come from; then the
# import rule of ADR 66, golangci-lint and revive. local/ is untracked.
GOPLS ?= gopls
GOLANGCI ?= golangci-lint
REVIVE ?= revive
lint: vuln
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	$(GO) vet ./...
	@out=$$($(GOPLS) check -severity=hint $$(git ls-files '*.go') 2>&1) && test -z "$$out" || { echo "$$out"; exit 1; }
	$(GO) list -f '{{.ImportPath}}{{range .Imports}} {{.}}{{end}}' ./... | awk -f tools/import-rule.awk
	$(GOLANGCI) run ./...
	$(REVIVE) -config revive.toml -set_exit_status -exclude ./local/... ./...

# Report module advisories too; fail on vulnerable symbols reachable by our code.
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest -scan=symbol -show=verbose ./...

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
