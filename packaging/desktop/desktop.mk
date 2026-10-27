# Native desktop targets are separate from the headless server build.
DESKTOP_VERSION ?= 0.1.0
DESKTOP_OUT ?= $(CURDIR)/dist
DESKTOP_GO_CACHE ?= $(CURDIR)/.local/go-build
DESKTOP_MOD_CACHE ?= $(CURDIR)/.local/go-mod
DESKTOP_SIGN_IDENTITY ?=
PYTHON ?= python3
ISCC ?= ISCC.exe

.PHONY: desktop-macos desktop-macos-dmg desktop-windows desktop-windows-installer desktop-windows-resources
desktop-macos desktop-macos-dmg: DESKTOP_ARCH ?= arm64
desktop-windows desktop-windows-installer desktop-windows-resources: DESKTOP_ARCH ?= amd64
desktop-macos desktop-macos-dmg desktop-windows desktop-windows-installer: export GOCACHE = $(DESKTOP_GO_CACHE)
desktop-macos desktop-macos-dmg desktop-windows desktop-windows-installer: export GOMODCACHE = $(DESKTOP_MOD_CACHE)
desktop-macos desktop-macos-dmg desktop-windows desktop-windows-installer: export GOARCH = $(DESKTOP_ARCH)
desktop-macos desktop-macos-dmg: export GOOS = darwin
desktop-macos desktop-macos-dmg: export CGO_ENABLED = 1
desktop-macos desktop-macos-dmg: export MACOSX_DEPLOYMENT_TARGET = 13.0
desktop-macos desktop-macos-dmg: export CGO_CFLAGS = -O2 -g -mmacosx-version-min=13.0
desktop-macos desktop-macos-dmg: export CGO_LDFLAGS = -O2 -g -mmacosx-version-min=13.0
desktop-windows desktop-windows-installer: export GOOS = windows
desktop-windows desktop-windows-installer: export CGO_ENABLED = 0
desktop-windows-installer: export MSYS_NO_PATHCONV = 1

desktop-macos:
	mkdir -p "$(DESKTOP_OUT)/macos-$(DESKTOP_ARCH)"
	$(GO) build -trimpath -ldflags "-s -w" -o "$(DESKTOP_OUT)/macos-$(DESKTOP_ARCH)/Alborz" ./cmd/alborz-desktop
	$(PYTHON) packaging/desktop/package.py --platform macos --arch "$(DESKTOP_ARCH)" --version "$(DESKTOP_VERSION)" --binary "$(DESKTOP_OUT)/macos-$(DESKTOP_ARCH)/Alborz" --output "$(DESKTOP_OUT)" --identity "$(DESKTOP_SIGN_IDENTITY)" $(DESKTOP_DMG)

desktop-macos-dmg: DESKTOP_DMG = --dmg
desktop-macos-dmg: desktop-macos

desktop-windows-resources: export GOOS = $(shell $(GO) env GOHOSTOS)
desktop-windows-resources: export GOARCH = $(shell $(GO) env GOHOSTARCH)
desktop-windows-resources:
	$(GO) run github.com/akavel/rsrc@v0.10.2 -arch "$(DESKTOP_ARCH)" -ico packaging/desktop/Alborz.ico -o "cmd/alborz-desktop/rsrc_windows_$(DESKTOP_ARCH).syso"

desktop-windows: desktop-windows-resources
	mkdir -p "$(DESKTOP_OUT)/windows-$(DESKTOP_ARCH)"
	$(GO) build -trimpath -ldflags "-s -w -H windowsgui" -o "$(DESKTOP_OUT)/windows-$(DESKTOP_ARCH)/Alborz.exe" ./cmd/alborz-desktop
	$(PYTHON) packaging/desktop/package.py --platform windows --arch "$(DESKTOP_ARCH)" --version "$(DESKTOP_VERSION)" --binary "$(DESKTOP_OUT)/windows-$(DESKTOP_ARCH)/Alborz.exe" --output "$(DESKTOP_OUT)"

desktop-windows-installer: desktop-windows
	"$(ISCC)" "/DAppVersion=$(DESKTOP_VERSION)" "/DArchitecture=$(DESKTOP_ARCH)" "/DBinary=$(DESKTOP_OUT)/windows-$(DESKTOP_ARCH)/Alborz.exe" "/O$(DESKTOP_OUT)" packaging/desktop/windows.iss
