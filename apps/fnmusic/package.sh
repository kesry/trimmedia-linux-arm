#!/bin/bash -e

export PS4='\e[33m+[\e[36m$(date "+%H:%M:%S")\e[33m]\e[0m '
set -x

cd "$(dirname "$0")"
ARCH=arm64
rm -rf bin/
CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} GO111MODULE=on \
  go build -ldflags "-s -w" \
  -o "bin/fnmusic.${ARCH}" .

if command -v upx >/dev/null 2>&1; then
  upx "bin/fnmusic.${ARCH}"
else
  echo "upx not found; skipping compression for bin/fnmusic.${ARCH}"
fi

echo "build success"

rm -rf output
mkdir -p output/temp
cp -v "bin/fnmusic.${ARCH}" output/temp/
cp -v init_data.sql install.sh output/temp/

# 如需随包携带 so，把对应文件放到本目录 lib/ 下即可被一起打包
if [ -d lib ]; then
  cp -av lib output/temp/
  tar -czvf output/fnmusic.tgz -C output/temp "fnmusic.${ARCH}" init_data.sql install.sh lib/
else
  tar -czvf output/fnmusic.tgz -C output/temp "fnmusic.${ARCH}" init_data.sql install.sh
fi

rm -rf output/temp
rm -rf bin/
echo "all done"
