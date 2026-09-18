#!/bin/bash -e
#
# install_deps.sh - 自动检测系统并安装依赖
#

cd "$(dirname "$0")"

set -euo pipefail

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'
NEED_EXTENDS="false"

# GitHub 下载代理前缀，可通过环境变量传入，如: PROXY_PREFIX=https://ghproxy.com ./install.sh
# 默认为空（直连），末尾的 / 会被自动去掉，拼接格式为 ${PROXY_PREFIX}${URL}
PROXY_PREFIX="${PROXY_PREFIX:-}"
PROXY_PREFIX="${PROXY_PREFIX%/}"

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
declare -a PACKAGES=()

case "${DISTRO_ID}" in
    debian)
        case "${DISTRO_VERSION}" in
            13)
                PACKAGES=(
                     sqlite3 openssl ca-certificates libass9 libbluray2
                     libmp3lame0  libopenmpt0t64 libopus0 libtcmalloc-minimal4t64
                     libtheora0 libvorbisenc2 libwebp7  libwebpmux3 libx264-164
                     libzvbi0t64 libnuma1 libavformat61 libavcodec61 libavutil59
                     libavfilter10 libswscale8 libswresample5 unzip wget
                )
                ;;
            *)
                warn "未针对 Debian ${DISTRO_VERSION} 配置依赖列表，尝试使用 Debian 13 安装"
                PACKAGES=(
                    sqlite3 unzip wget
                )
                NEED_EXTENDS="true"
                ;;
        esac
        PKG_MANAGER="apt"
        ;;

    ubuntu)
        case "${DISTRO_VERSION}" in
            *)
                warn "未针对 Ubuntu ${DISTRO_VERSION} 配置依赖列表，尝试使用 Ubuntu 26.04 安装"
                PACKAGES=(
                    sqlite3 unzip wget
                )
                NEED_EXTENDS="true"
                ;;
        esac
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

# 下载依赖包
if [ "${NEED_EXTENDS}" = "true" ]; then
    wget "${PROXY_PREFIX}https://github.com/kesry/trimmedia-linux-arm/releases/download/v1/lib.extends.zip"
    unzip lib.extends.zip
    rm lib.extends.zip
fi


wget "${PROXY_PREFIX}https://github.com/kesry/trimmedia-linux-arm/releases/download/v1/trim-media-lib.zip"
unzip trim-media-lib.zip
rm trim-media-lib.zip

wget "${PROXY_PREFIX}https://github.com/kesry/trimmedia-linux-arm/releases/download/v1/trim.media.tar.gz"
tar xzvf trim.media.tar.gz
rm trim.media.tar.gz

if [ ! -e "./fntv.arm64" ]; then
    wget "${PROXY_PREFIX}https://github.com/kesry/trimmedia-linux-arm/releases/download/v1/fntv.arm64"
    chmod +x fntv.arm64
fi

info "依赖安装完成 ✅"
