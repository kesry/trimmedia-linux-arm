#!/bin/bash -e

set -xeuo pipefail

cd "$(dirname "$0")"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
NEED_EXTENDS="false"


info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }


# 检测发行版和版本
detect_distro() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        DISTRO_ID="${ID:-unknown}"
        DISTRO_VERSION="${VERSION_ID:-unknown}"
        DISTRO_CODENAME="${VERSION_CODENAME:-unknown}"
    else
        error "无法识别系统发行版 (/etc/os-release 不存在)"
        exit 1
    fi
}

detect_distro
info "检测到系统: ${DISTRO_ID} ${DISTRO_VERSION} (${DISTRO_CODENAME})"

case "${DISTRO_ID}" in
    debian|ubuntu)
        PKG_MANAGER="apt"
        ;;
    *)
        error "不支持的发行版: ${DISTRO_ID}"
        exit 1
        ;;
esac


# 执行安装
if command -v wget >/dev/null 2>&1; then
    info "找到wget命令"
else
    error "需要 wget 命令"
    exit 1
fi

PROXY_PREFIX="${PROXY_PREFIX:-}"
if [ -z "${PROXY_PREFIX}" ]; then
    PROXY_PREFIX=""
else
    PROXY_PREFIX="${PROXY_PREFIX%/}/"
fi

# 下载trim-media-lib.zip
if [ ! -d "lib" ] || [ ! -d "bin" ]; then
    rm -rf lib bin
    rm -rf trim-media-lib.zip
    wget -4 "${PROXY_PREFIX}https://github.com/kesry/trimmedia-linux-arm/releases/download/v1/trim-media-lib.zip"
    unzip trim-media-lib.zip
    rm -rf trim-media-lib.zip
fi

# 下载扩展包
if [ ! -d "extends" ]; then
    rm -rf extends/
    rm -rf lib.extends.zip
    wget -4 "${PROXY_PREFIX}https://github.com/kesry/trimmedia-linux-arm/releases/download/v1/lib.extends.zip"
    unzip lib.extends.zip
    rm -rf lib.extends.zip
fi

if [ -e "nodri.so" ]; then
    mv nodri.so lib/
fi

chmod +x ./mediasrv.arm64

info "依赖安装完成 ✅"