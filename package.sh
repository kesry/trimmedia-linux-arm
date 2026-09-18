#!/bin/sh -e

rm -rf out/

cd "$(dirname "$0")"
ARCH="arm64"
sh lib/rpcbroker/build.sh

mkdir -p out/temp
mv "lib/rpcbroker/bin/fntv.${ARCH}" out/temp
# tar -xzvf dependence/trim.media.tar.gz -C out/temp
# unzip dependence/trim-media-lib.zip -d out/temp
# unzip dependence/lib.extends.zip -d out/temp
cp install.sh init.sql out/temp
chmod +x out/temp/install.sh

cd out/temp
tar -czvf ../fntv.linux.arm64.tar.gz ./
cd ../ && rm -rf temp