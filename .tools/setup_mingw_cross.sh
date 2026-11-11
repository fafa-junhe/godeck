#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CROSS_DIR="$PROJECT_ROOT/.tools/mingw-cross"
mkdir -p "$CROSS_DIR"
cd "$CROSS_DIR"

ARCH_MIRROR="https://mirrors.tuna.tsinghua.edu.cn/archlinux/extra/os/x86_64"

PKGS=(
    "mingw-w64-binutils-2.47-1-x86_64.pkg.tar.zst"
    "mingw-w64-headers-14.0.0-1-any.pkg.tar.zst"
    "mingw-w64-crt-14.0.0-1-any.pkg.tar.zst"
    "mingw-w64-gcc-16.2.0-2-x86_64.pkg.tar.zst"
)

for pkg in "${PKGS[@]}"; do
    if [ ! -f "$pkg" ]; then
        echo "Downloading $pkg..."
        curl -s -f -L -o "$pkg" "$ARCH_MIRROR/$pkg"
    fi
    echo "Extracting $pkg..."
    tar -I zstd -xf "$pkg" --overwrite
done

echo "MinGW-w64 cross toolchain unpacked successfully."
