#!/bin/sh -e

cd "$(dirname "$0")"

ARCH="arm64"

echo "building rpcbroker..."

rm go.mod
go mod init rpcbroker
go mod tidy

rm -rf bin
mkdir -p bin

CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} GO111MODULE=on \
  go build -ldflags "-s -w" \
  -o "bin/fntv.${ARCH}" .

if command -v upx >/dev/null 2>&1; then
  upx "bin/fntv.${ARCH}"
else
  echo "upx not found; skipping compression for bin/fntv.${ARCH}"
fi




