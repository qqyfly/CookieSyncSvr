BINARY_NAME := cookiesyncsvr
VERSION     := 0.1.0
DIST_DIR    := dist
GO_FLAGS    := -trimpath -ldflags="-s -w"

.PHONY: all build test clean package-arch package-deb packages

all: test build packages

## 1. Build Original Static Executable (CGO_ENABLED=0 for Arch Linux & Debian VPS portability)
build:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GO_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME) ./cmd/cookiesyncsvr
	@echo "==> Built static executable: $(DIST_DIR)/$(BINARY_NAME)"

## Run Unit & Integration Tests (pure Go, no CGO)
test:
	CGO_ENABLED=0 go test -v ./...

## 2. Way 1: Build Arch Linux Package (.pkg.tar.zst) via makepkg
package-arch: build
	cd packaging/arch && makepkg -sf --noconfirm
	mv packaging/arch/$(BINARY_NAME)-*.pkg.tar.* $(DIST_DIR)/
	@echo "==> Arch Linux package moved to $(DIST_DIR)/"

## 3. Way 2: Build Debian Package (.deb) for Debian VPS
package-deb: build
	chmod +x packaging/debian/build-deb.sh
	PKG_VERSION=$(VERSION) ./packaging/debian/build-deb.sh

packages: package-arch package-deb

clean:
	rm -rf $(DIST_DIR) packaging/arch/pkg packaging/arch/src *.db
