#!/bin/sh
set -e +x

cd "$(dirname "$0")"

. ./build.sh

go run github.com/Jorropo/jhttp
