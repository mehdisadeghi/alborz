GO ?= go
ADDR ?= localhost:1323
# Served mail domains, bare for SRV discovery or with explicit upstreams:
# ARGS = example.org example.com=imaps://mail.example.com example.com=smtps://mail.example.com
ARGS ?=

.PHONY: build run watch fmt test login-key

build:
	$(GO) build -o alborz ./cmd/alborz

run: build
	./alborz -theme alborz -addr $(ADDR) $(ARGS)

# Rebuild and restart on Go changes; reload templates in the running
# server (SIGUSR1, keeps sessions) on theme changes.
watch:
	find themes plugins -name '*.html' | entr -n pkill -USR1 -x alborz & \
	find . -name '*.go' | entr -nr $(MAKE) run

fmt:
	gofmt -w .

# Templates, locales, and every page a signed-in reader can open, served
# by an in-process IMAP server. No network, no rig, no credentials.
test:
	$(GO) test ./...

# Generate a Fernet key for -login-key.
login-key:
	$(GO) run github.com/fernet/fernet-go/cmd/fernet-keygen
