#!/bin/sh
set -e +x

cd "$(dirname "$0")"

set -x

GOOS=js GOARCH=wasm go build -o main.wasm .
cp "$(go env GOROOT)/misc/wasm/wasm_exec.js" wasm_exec.js
