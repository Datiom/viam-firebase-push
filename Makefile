GO_BUILD_ENV := CGO_ENABLED=0
GO_BUILD_FLAGS := -ldflags="-s -w"
MODULE_BINARY := bin/firebase-push

# Viam cloud build sets VIAM_TARGET_OS per target platform.
# Windows is cross-compiled and needs a .exe suffix plus a CGO-free build.
# viam-server resolves the entrypoint from the meta.json, so the Windows
# tarball must ship a meta.json whose entrypoint names the .exe —
# see fix-meta-for-win below.
ifeq ($(VIAM_TARGET_OS), windows)
	GO_BUILD_ENV += GOOS=windows GOARCH=amd64
	GO_BUILD_FLAGS := -ldflags="-s -w" -tags no_cgo
	MODULE_BINARY = bin/firebase-push.exe
endif

.PHONY: build lint update test module.tar.gz fix-meta-for-win packages module all setup clean upload

build: $(MODULE_BINARY)

# Single-arch host/dev build (e.g. `make bin/firebase-push`).
$(MODULE_BINARY): Makefile go.mod *.go cmd/module/*.go
	GOOS=$(VIAM_BUILD_OS) GOARCH=$(VIAM_BUILD_ARCH) $(GO_BUILD_ENV) go build $(GO_BUILD_FLAGS) -o $(MODULE_BINARY) ./cmd/module

lint:
	gofmt -s -w .

update:
	go get go.viam.com/rdk@latest
	go mod tidy

test:
	go test ./...

FIRST_RUN := $(shell jq -r '.first_run // empty' meta.json 2>/dev/null)
TAR_FILES := meta.json $(MODULE_BINARY)
ifneq ($(FIRST_RUN),)
TAR_FILES += $(FIRST_RUN)
endif

module.tar.gz: build
	tar -czf module.tar.gz $(TAR_FILES)
	-test -e .git/config && git checkout meta.json

# On Windows cloud builds, rewrite the entrypoint to the .exe before packaging;
# the trailing `git checkout meta.json` above restores the working copy afterward.
ifeq ($(VIAM_TARGET_OS), windows)
module.tar.gz: fix-meta-for-win
endif

fix-meta-for-win:
	jq '.entrypoint = "bin/firebase-push.exe"' meta.json > temp.json && mv temp.json meta.json

# `make packages` cross-compiles every architecture locally and produces
# one tarball per arch at bin/<goos>-<goarch>/module.tar.gz, ready for `make upload`.
# Each tarball contains `bin/firebase-push` and `meta.json` at the top
# level, matching the entrypoint path declared in meta.json.
packages: bin/linux-amd64/module.tar.gz bin/linux-arm64/module.tar.gz bin/darwin-arm64/module.tar.gz bin/windows-amd64/module.tar.gz

bin/%/module.tar.gz: Makefile go.mod *.go cmd/module/*.go meta.json
	@set -e; os=$$(echo $* | cut -d- -f1); arch=$$(echo $* | cut -d- -f2); \
	  workdir=bin/$*; bin=firebase-push; ext=""; tags=""; \
	  if [ "$$os" = "windows" ]; then ext=".exe"; tags="-tags no_cgo"; fi; \
	  mkdir -p $$workdir/bin; \
	  echo ">> building $$os/$$arch -> $$workdir/bin/$$bin$$ext"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build $(GO_BUILD_FLAGS) $$tags -o $$workdir/bin/$$bin$$ext ./cmd/module; \
	  if [ "$$os" = "windows" ]; then \
	    jq '.entrypoint = "bin/'$$bin'.exe"' meta.json > $$workdir/meta.json; \
	  else \
	    cp meta.json $$workdir/meta.json; \
	  fi; \
	  tar -czf $$workdir/module.tar.gz -C $$workdir bin/$$bin$$ext meta.json

module: test module.tar.gz

all: test packages

setup:
	go mod tidy

clean:
	rm -rf bin module.tar.gz

VERSION ?= 0.1.0

# Build every arch, then print the upload commands. Override the version with
# e.g. `make upload VERSION=1.2.3`.
upload: packages
	@echo viam module upload --version \"$(VERSION)\" --platform \"linux/amd64\" bin/linux-amd64/module.tar.gz
	@echo viam module upload --version \"$(VERSION)\" --platform \"linux/arm64\" bin/linux-arm64/module.tar.gz
	@echo viam module upload --version \"$(VERSION)\" --platform \"darwin/arm64\" bin/darwin-arm64/module.tar.gz
	@echo viam module upload --version \"$(VERSION)\" --platform \"windows/amd64\" bin/windows-amd64/module.tar.gz
