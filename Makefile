APP_NAME := file-share
DIST_DIR := dist
MAIN_PKG := .

GO ?= go
GOFLAGS ?=
LDFLAGS ?= -s -w

.PHONY: all build build-amd64 build-arm64 clean dist

all: build

build: build-amd64 build-arm64

dist:
	@mkdir -p $(DIST_DIR)

build-amd64: dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(APP_NAME)-linux-amd64 $(MAIN_PKG)

build-arm64: dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(APP_NAME)-linux-arm64 $(MAIN_PKG)

clean:
	rm -f $(DIST_DIR)/$(APP_NAME)-linux-amd64 $(DIST_DIR)/$(APP_NAME)-linux-arm64