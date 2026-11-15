GO ?= go
NODE ?= node
ADDR ?= localhost:1323
# Served mail domains, bare for SRV discovery or with explicit upstreams:
# ARGS = example.org example.com=imaps://mail.example.com example.com=smtps://mail.example.com
ARGS ?=

.PHONY: all build run watch fmt lint vuln test test-js login-key

all: build

build:
	$(GO) build -o alborz ./cmd/alborz

run: build
	./alborz -debug -addr $(ADDR) $(ARGS)

# Rebuild and restart on Go changes; reload templates in the running
# server (SIGUSR1, keeps sessions) on template changes.
watch:
	@command -v entr >/dev/null || { echo "missing: entr (for watch) — install entr, see make doctor"; exit 1; }
	find templates -name '*.html' | entr -n pkill -USR1 -x alborz & \
	find . -name '*.go' | entr -nr $(MAKE) run

# Follow local Go packages, including files that have not been tracked yet.
GO_DIRS := $(shell $(GO) list -e -f '{{.Dir}}' ./...)
GO_FILES := $(wildcard $(addsuffix /*.go,$(GO_DIRS)))

fmt:
	gofmt -w $(GO_FILES)

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
	$(STYLELINT) "templates/**/*.css" "addons/*/web/**/*.css"
lint-go:
	@out=$$(gofmt -l $(GO_FILES)) && test -z "$$out" || { echo "$$out"; exit 1; }
	$(GO) vet ./...
	@out=$$($(GOPLS) check -severity=hint $(GO_FILES) 2>&1) && test -z "$$out" || { echo "$$out"; exit 1; }
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

# Debian and Red Hat packages in dist/, e.g.
# make packages NAME=alborz-minimal GOARCH=arm GOARM=7
.PHONY: packages
packages: export NAME ?= alborz
packages: export VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always))
packages: export ARCH = $(GOARCH)$(GOARM)
packages: GOARCH ?= $(shell $(GO) env GOHOSTARCH)
packages:
	GOOS=linux GOARCH=$(GOARCH) GOARM=$(GOARM) CGO_ENABLED=0 $(GO) build $(if $(filter alborz-minimal,$(NAME)),-tags noaddons) -trimpath -ldflags "-s -w" -o dist/alborz ./cmd/alborz
	$(TOOL) nfpm package -f contrib/packaging/nfpm.yaml -p deb -t dist
	$(TOOL) nfpm package -f contrib/packaging/nfpm.yaml -p rpm -t dist

include contrib/packaging/desktop/desktop.mk

# Check commands used by the targets in this Makefile without installing anything.
DOCTOR_NEEDS = go:build,test,lint node:test-js,lint-web npx:lint-web git:version,release-next openssl:login-key entr:watch
CHROMES = chrome-headless-shell google-chrome chromium chromium-browser chrome
CHROME_APPS = '/Applications/Google Chrome.app' /Applications/Chromium.app

.PHONY: doctor
doctor:
	@missing=0; for need in $(DOCTOR_NEEDS); do \
		command -v $${need%%:*} >/dev/null || { echo "missing: $${need%%:*} (for $${need#*:})"; missing=1; }; \
	done; \
	found=; for c in $(CHROMES); do command -v $$c >/dev/null && found=$$c; done; \
	for a in $(CHROME_APPS); do test -d "$$a" && found="$$a"; done; \
	test -n "$$found" || { echo 'missing: Chrome or chrome-headless-shell (for shot)'; missing=1; }; \
	want=$$(sed -n 's/^go //p' go.mod); have=$$($(GO) env GOVERSION 2>/dev/null); \
	echo "go.mod wants go $$want; $(GO) is $${have:-absent}"; \
	test $$missing = 0 && echo 'nothing missing'

# Releases. A version is an annotated git tag; CI reads the tag name and stamps it into the binary.
.PHONY: version release-next release-patch release-minor release-major

version:
	@printf 'describe : %s\n' "$$(git describe --tags --always --dirty 2>/dev/null)"
	@printf 'would be : %s\n' "$$(git describe --tags --exact-match 2>/dev/null || echo 'dev (untagged commit)')"

LAST_TAG = $(shell git tag -l 'v*' --sort=-v:refname | head -1)

release-next:
	@last='$(LAST_TAG)'; last=$${last:-v0.0.0}; \
	set -- $$(echo "$${last#v}" | tr . ' '); \
	printf 'last  : %s\n' "$$last"; \
	printf 'patch : v%s.%s.%s\n' "$$1" "$$2" "$$(($$3 + 1))"; \
	printf 'minor : v%s.%s.0\n' "$$1" "$$(($$2 + 1))"; \
	printf 'major : v%s.0.0\n' "$$(($$1 + 1))"

release-patch: BUMP = patch
release-minor: BUMP = minor
release-major: BUMP = major
release-patch release-minor release-major:
	@test -z "$$(git status --porcelain --untracked-files=no)" \
		|| { echo 'refusing: the working tree has uncommitted changes'; exit 1; }
	@last='$(LAST_TAG)'; last=$${last:-v0.0.0}; \
	set -- $$(echo "$${last#v}" | tr . ' '); \
	case '$(BUMP)' in \
		patch) next="v$$1.$$2.$$(($$3 + 1))" ;; \
		minor) next="v$$1.$$(($$2 + 1)).0" ;; \
		major) next="v$$(($$1 + 1)).0.0" ;; \
	esac; \
	echo "tagging $$next at $$(git log -1 --format='%h %s')"; \
	git tag -a "$$next" -m "$$next"

-include local.mk
