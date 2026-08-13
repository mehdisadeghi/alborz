GO ?= go
ADDR ?= localhost:1323
# Upstream servers, e.g.:
# ARGS = imaps://mail.example.org smtps://mail.example.org sieve://mail.example.org
ARGS ?=

.PHONY: build run watch fmt login-key

build:
	$(GO) build -o alps ./cmd/alps

run: build
	./alps -addr $(ADDR) $(ARGS)

# Rebuild and restart on Go changes; reload templates in the running
# server (SIGUSR1, keeps sessions) on theme changes.
watch:
	find themes plugins -name '*.html' | entr -n pkill -USR1 -x alps & \
	find . -name '*.go' | entr -nr $(MAKE) run

fmt:
	gofmt -w .

# Generate a Fernet key for -login-key.
login-key:
	$(GO) run github.com/fernet/fernet-go/cmd/fernet-keygen
