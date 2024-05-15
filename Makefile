export GOBIN := $(abspath .)/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: install
install:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.0
	go install go.uber.org/mock/mockgen@v0.4.0

.PHONY: generate
generate: install
	go generate ./...
