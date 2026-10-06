#!/bin/bash -e

export PS4='\e[33m+[\e[36m$(date "+%H:%M:%S")\e[33m]\e[0m '
set -x

cd "$(dirname "$0")"
ARCH=arm64
rm -rf bin/
CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} GO111MODULE=on \
  go build -ldflags "-s -w" \
  -o "bin/mediasrv.${ARCH}" .

if command -v upx >/dev/null 2>&1; then
  upx "bin/mediasrv.${ARCH}"
else
  echo "upx not found; skipping compression for bin/mediasrv.${ARCH}"
fi

echo "build success"

rm -rf output
mkdir -p output/temp
cp -v "bin/mediasrv.${ARCH}" output/temp/
cp -v install.sh output/temp/

gcc -shared -fPIC -o output/temp/nodri.so ../../nodri.c
gcc -shared -fPIC -o output/temp/fakecompat.so ../../fakecompat.c
gcc -shared -fPIC -o output/temp/nodmaheap.so ../../nodmaheap.c

tar -czvf output/mediasrv.tgz -C output/temp "mediasrv.${ARCH}" install.sh nodri.so fakecompat.so nodmaheap.so
rm -rf output/temp
rm -rf bin/
echo "all done"
