#!/bin/bash
# ==============================================================================
# GoDeck Arch Linux Package Build Script
# Builds official .pkg.tar.zst package using standard makepkg in WSL Arch
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

PKGNAME="godeck"
PKGVER="0.1.0.r20261107"
PKGREL="13"
ARCH="x86_64"
PKG_FILE="${PKGNAME}-${PKGVER}-${PKGREL}-${ARCH}.pkg.tar.zst"

DIST_DIR="$PROJECT_ROOT/dist/arch"
BUILD_DIR="/home/build/godeck-package"

echo "=== 1. 检查构建环境与工具 ==="
if [ "$(id -u)" -ne 0 ]; then
    echo "提示: 请以 root 身份运行此脚本（将自动降权至 build 用户执行 makepkg）"
fi

if ! command -v makepkg >/dev/null 2>&1; then
    echo "错误: 未检测到 makepkg，请在 Arch 环境中执行" >&2
    exit 1
fi

if ! id -u build >/dev/null 2>&1; then
    echo "错误: 未检测到 build (UID 1000) 用户，请先创建非 root 构建用户" >&2
    exit 1
fi

echo "=== 2. 准备独立 Linux 临时构建目录 ==="
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"
mkdir -p "$DIST_DIR"

echo "=== 3. 打包干净源码归档 (保留中文字体与必要组件，排除构建副产物) ==="
SRC_TAR="$BUILD_DIR/${PKGNAME}-${PKGVER}.tar.gz"
tar -czf "$SRC_TAR" \
    --transform="s|^|${PKGNAME}-${PKGVER}/|" \
    --exclude=".git" \
    --exclude=".tools" \
    --exclude=".codex" \
    --exclude="dist" \
    --exclude="deploy" \
    --exclude="GoDeck.bat" \
    --exclude="GoDeck.cmd" \
    --exclude="godeck" \
    --exclude="godeck-compat" \
    --exclude="godeck-compat.exe" \
    --exclude="godeck-hub" \
    --exclude="*.log" \
    --exclude="*.tmp" \
    -C "$PROJECT_ROOT" \
    main.go go.mod go.sum internal native web packaging third_party

# 复制打包定义文件
cp "$SCRIPT_DIR/PKGBUILD" "$BUILD_DIR/PKGBUILD"
cp "$SCRIPT_DIR/godeck.desktop" "$BUILD_DIR/godeck.desktop"
cp "$SCRIPT_DIR/godeck.svg" "$BUILD_DIR/godeck.svg"
cp "$SCRIPT_DIR/README.md" "$BUILD_DIR/README.md"

# 设置构建目录权限为 build 用户
chown -R build:build "$BUILD_DIR"

# 确保 build 用户的 Go 模块缓存与系统已下载的模块一致，避免外网网络波动导致失败
mkdir -p /home/build/go/pkg/mod
if [ -d /root/go/pkg/mod ]; then
    cp -rn /root/go/pkg/mod/* /home/build/go/pkg/mod/ 2>/dev/null || true
    chown -R build:build /home/build/go
fi

echo "=== 4. 以 build 用户运行标准 makepkg ==="
# 注意：使用 --nodeps 跳过构建机器本地依赖检查（避免在构建容器中安装整个 KDE 桌面环境）
# 真实运行时依赖完整保存在 PKGBUILD 中并由 makepkg 写入 .PKGINFO
runuser -u build -- bash -c "
    cd '$BUILD_DIR' && \
    makepkg --nodeps --nocheck -f
"

echo "=== 5. 归档交付包至 dist/arch/ ==="
if [ ! -f "$BUILD_DIR/$PKG_FILE" ]; then
    echo "错误: 未在构建目录中找到生成的包文件 $BUILD_DIR/$PKG_FILE" >&2
    exit 1
fi

cp "$BUILD_DIR/$PKG_FILE" "$DIST_DIR/$PKG_FILE"
chmod 644 "$DIST_DIR/$PKG_FILE"

echo "=== 构建完成 ==="
echo "交付产物路径: $DIST_DIR/$PKG_FILE"
ls -lh "$DIST_DIR/$PKG_FILE"
