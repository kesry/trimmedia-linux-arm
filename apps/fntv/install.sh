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

# 检查是否为 root（或使用 sudo）
if [[ $EUID -ne 0 ]]; then
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
    else
        error "需要 root 权限运行，且未找到 sudo"
        exit 1
    fi
else
    SUDO=""
fi

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

# 根据发行版选择依赖包列表
declare -a PACKAGES=(sqlite3 wget libc6)

case "${DISTRO_ID}" in
    debian|ubuntu)
        PKG_MANAGER="apt"
        ;;
    *)
        error "不支持的发行版: ${DISTRO_ID}"
        exit 1
        ;;
esac

if [[ ${#PACKAGES[@]} -eq 0 ]]; then
    error "依赖包列表为空"
    exit 1
fi

# 执行安装
info "使用 ${PKG_MANAGER} 安装 ${#PACKAGES[@]} 个依赖包..."
info "包列表: ${PACKAGES[*]}"

case "${PKG_MANAGER}" in
    apt)
        ${SUDO} apt-get update
        ${SUDO} apt-get install -y --no-install-recommends "${PACKAGES[@]}"
        ;;
    *)
        error "未知的包管理器: ${PKG_MANAGER}"
        exit 1
        ;;
esac

PROXY_PREFIX="${PROXY_PREFIX:-}"
if [ -z "${PROXY_PREFIX}" ]; then
    PROXY_PREFIX=""
else
    PROXY_PREFIX="${PROXY_PREFIX%/}/"
fi

# 下载trim.media.tar.gz
if [ ! -d "trim.media" ]; then 
    rm -rf trim.media.tar.gz
    wget -4 "${PROXY_PREFIX}https://github.com/kesry/trimmedia-linux-arm/releases/download/v1/trim.media.tar.gz"
    tar xzvf trim.media.tar.gz
    rm trim.media.tar.gz
fi

chmod +x fntv.arm64

info "依赖安装完成 ✅"