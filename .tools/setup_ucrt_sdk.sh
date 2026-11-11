#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SDK_DIR="$PROJECT_ROOT/.tools/windows-sdk"
mkdir -p "$SDK_DIR"
cd "$SDK_DIR"

BASE_URL="https://mirrors.tuna.tsinghua.edu.cn/msys2/mingw/ucrt64"

PACKAGES=(
    "mingw-w64-ucrt-x86_64-gstreamer-1.24.12-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-gst-plugins-base-1.24.12-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-gst-plugins-bad-1.24.12-3-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-gst-plugins-bad-libs-1.24.12-3-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-gst-plugins-ugly-1.24.12-2-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-gst-plugins-good-1.24.12-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-glib2-2.82.5-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-orc-0.4.44-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-gettext-runtime-1.0-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-libwinpthread-git-12.0.0.r747.g1a99f8514-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-pcre2-10.49-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-libffi-3.8.0-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-zlib-1.3.2-3-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-x264-0.165.r3222.b35605a-3-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-gcc-libs-16.2.0-6-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-libiconv-1.19.1-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-bzip2-1.0.8-4-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-openh264-2.7.0-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-graphene-1.10.8-3-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-libpng-1.6.59-2-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-libogg-1.3.6-1-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-libvorbis-1.3.7-3-any.pkg.tar.zst"
    "mingw-w64-ucrt-x86_64-libtheora-1.2.0-1-any.pkg.tar.zst"
)

for pkg in "${PACKAGES[@]}"; do
    if [ ! -f "$pkg" ]; then
        echo "Downloading $pkg..."
        curl -s -f -L -o "$pkg" "$BASE_URL/$pkg"
    fi
    echo "Extracting $pkg..."
    tar -I zstd -xf "$pkg" --overwrite
done

echo "All 23 MSYS2 UCRT64 GStreamer packages unpacked into $SDK_DIR/ucrt64"
