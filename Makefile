export GOBIN := $(abspath .)/bin
export PATH := $(GOBIN):$(PATH)

ifeq ($(OS),Windows_NT)
	UNAME_OS := windows
	ifeq ($(PROCESSOR_ARCHITECTURE),AMD64)
		UNAME_ARCH := x86_64
	endif
	ifeq ($(PROCESSOR_ARCHITECTURE),ARM64)
		UNAME_ARCH := aarch64
	endif
	PROTOC_BUILD := win64

	BIN_DIR := $(abspath .)/bin
	export PATH := $(BIN_DIR);$(PATH)
	TMP_PROTOC := $(TEMP)/protoc-$(RANDOM)
else
	UNAME_OS := $(shell uname -s)
	UNAME_ARCH := $(shell uname -m)
	PROTOC_BUILD := $(shell echo ${UNAME_OS}-${UNAME_ARCH} | tr '[:upper:]' '[:lower:]' | sed 's/darwin/osx/' | sed 's/arm64/aarch_64/' | sed 's/aarch64/aarch_64/')

 	BIN_DIR := $(abspath .)/bin
 	export PATH := $(BIN_DIR):$(PATH)
 	TMP_PROTOC := $(shell mktemp -d)
endif

.PHONY: install-protoc
install-protoc: protoc-plugins
	@mkdir -p $(BIN_DIR)
ifeq ($(OS),Windows_NT)
	@mkdir -p $(TMP_PROTOC)
endif
	curl -sSL https://github.com/protocolbuffers/protobuf/releases/download/v26.0/protoc-26.0-${PROTOC_BUILD}.zip -o $(TMP_PROTOC)/protoc.zip
	@unzip $(TMP_PROTOC)/protoc.zip -d $(TMP_PROTOC)
	@cp -f $(TMP_PROTOC)/bin/protoc $(BIN_DIR)/protoc
	@chmod +x $(BIN_DIR)/protoc

protoc-plugins:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.0

.PHONY: install
install: install-protoc
	go install go.uber.org/mock/mockgen@v0.4.0

.PHONY: generate
generate:
	go generate ./...
