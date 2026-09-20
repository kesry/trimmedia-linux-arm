#!/bin/bash -e

export PS4='\e[33m+[\e[36m$(date "+%H:%M:%S")\e[33m]\e[0m '
set -x

cd "$(dirname "$0")"
ARCH=arm64
rm -rf bin/
CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} GO111MODULE=on \
  go build -ldflags "-s -w" \
  -o "bin/fntv.${ARCH}" .

if command -v upx >/dev/null 2>&1; then
  upx "bin/fntv.${ARCH}"
else
  echo "upx not found; skipping compression for bin/fntv.${ARCH}"
fi

echo "build success"

rm -rf output
mkdir -p output/temp
cp -v "bin/fntv.${ARCH}" output/temp/
cp -v init.sql install.sh output/temp/
cp -av lib output/temp/

tar -czvf output/fntv.tgz -C output/temp "fntv.${ARCH}" init.sql install.sh lib/
rm -rf output/temp
rm -rf bin/
echo "all done"