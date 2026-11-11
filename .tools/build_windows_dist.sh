#!/bin/bash
# ==============================================================================
# GoDeck Windows amd64 完整客户端构建与绿色打包脚本 (UCRT64 完整闭包架构)
#
# 构建规范与证据说明：
# 1. 跨平台编译器：Arch Linux 官方 mingw-w64-gcc 16.2.0 + GNU binutils 2.47
# 2. 运行时与媒体库：MSYS2 官方 UCRT64 GStreamer 1.28.8-1、GLib 2.90.1-1、FFmpeg 9.0.2-2
#    及配套 201 个官方发行包的完整递归依赖闭包（位于 .tools/windows-sdk/ucrt64/）。
# 3. 根因确证：经只读 PE 导入表诊断，此前弹出的 DllMain 报错系因混用旧版 1.24.12 动态库
#    （该版本错误导入 libintl-8.dll!DllMain），现已全链路统一为官方无 DllMain 缺陷的完整发行包。
# 4. 私有构建缓存：采用独立的 .tools/gocache，强制执行 -a 全量重新编译，杜绝旧 cgo 缓存复用。
# 5. 安全 Staging 打包：先在 dist/.staging-windows-amd64 中完整组装，通过路径边界防护
#    安全备份后原子替换正式交付目录 dist/windows-amd64。
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$PROJECT_ROOT"

DIST_PARENT="$PROJECT_ROOT/dist"
DIST_DIR="$DIST_PARENT/windows-amd64"
STAGING_DIR="$DIST_PARENT/.staging-windows-amd64"
SDK_DIR="$PROJECT_ROOT/.tools/windows-sdk/ucrt64"
CROSS_DIR="$PROJECT_ROOT/.tools/mingw-cross/usr"
GOCACHE_DIR="$PROJECT_ROOT/.tools/gocache"

# 严格边界安全检查
assert_safe_dist_path() {
    local target="$1"
    if [ -z "$target" ]; then
        echo "安全拦截: 路径为空，禁止操作" >&2
        exit 1
    fi
    local canonical_target
    canonical_target="$(readlink -f "$target" 2>/dev/null || realpath -m "$target" 2>/dev/null || echo "$target")"
    local canonical_parent
    canonical_parent="$(readlink -f "$DIST_PARENT" 2>/dev/null || realpath -m "$DIST_PARENT" 2>/dev/null || echo "$DIST_PARENT")"
    local canonical_root
    canonical_root="$(readlink -f "$PROJECT_ROOT" 2>/dev/null || realpath -m "$PROJECT_ROOT" 2>/dev/null || echo "$PROJECT_ROOT")"

    if [ "$canonical_target" = "/" ] || [ "$canonical_target" = "$canonical_root" ] || [ "$canonical_target" = "$canonical_parent" ]; then
        echo "安全拦截: 目标路径 $canonical_target 触碰保护边界，禁止操作" >&2
        exit 1
    fi
    case "$canonical_target" in
        "$canonical_parent"/*)
            ;;
        *)
            echo "安全拦截: 目标路径 $canonical_target 超出 dist/ 目录边界，禁止操作" >&2
            exit 1
            ;;
    esac
}

echo "=== 1. 检查必要工具链与 SDK ==="
if [ ! -f "$CROSS_DIR/bin/x86_64-w64-mingw32-gcc" ]; then
    echo "错误: 未找到 x86_64-w64-mingw32-gcc，请先运行 setup_mingw_cross.sh" >&2
    exit 1
fi

if [ ! -d "$SDK_DIR/bin" ]; then
    echo "错误: 未找到有效 UCRT64 GStreamer SDK 目录 $SDK_DIR" >&2
    exit 1
fi

export PATH="$CROSS_DIR/bin:/usr/sbin:/usr/bin:$PATH"
export PKG_CONFIG="$PROJECT_ROOT/.tools/pkg-config-windows.sh"
export PKG_CONFIG_PATH="$SDK_DIR/lib/pkgconfig"
export CGO_ENABLED=1
export GOOS=windows
export GOARCH=amd64
export CC="x86_64-w64-mingw32-gcc"
export GOCACHE="$GOCACHE_DIR"

mkdir -p "$GOCACHE_DIR"
mkdir -p "$DIST_PARENT"
assert_safe_dist_path "$STAGING_DIR"

echo "=== 2. 初始化 Safe Staging 目录 ==="
rm -rf "$STAGING_DIR"
mkdir -p "$STAGING_DIR/gstreamer-1.0"
mkdir -p "$STAGING_DIR/libexec/gstreamer-1.0"
mkdir -p "$STAGING_DIR/licenses"

echo "=== 2.5 编译 Windows 应用程序图标资源 (.syso) ==="
if [ -f "$PROJECT_ROOT/packaging/godeck.ico" ]; then
    "$CROSS_DIR/bin/x86_64-w64-mingw32-windres" -i "$PROJECT_ROOT/godeck_windows.rc" -O coff -o "$PROJECT_ROOT/godeck_windows_amd64.syso"
fi

echo "=== 3. 编译 GoDeck.exe (剥离 DWARF 符号 -s -w，启用 windowsgui 纯 GUI 子系统) ==="
GOPROXY=https://goproxy.cn,direct GOMAXPROCS=2 go build \
    -a \
    -buildmode=exe \
    -ldflags="-H=windowsgui -linkmode=external -s -w" \
    -tags novulkan \
    -p 1 \
    -o "$STAGING_DIR/GoDeck.exe" .

echo "=== 4. 同步 GStreamer 插件扫描器 ==="
cp -f "$SDK_DIR/libexec/gstreamer-1.0/gst-plugin-scanner.exe" "$STAGING_DIR/libexec/gstreamer-1.0/"
cp -f "$SDK_DIR/libexec/gstreamer-1.0/gst-plugin-scanner.exe" "$STAGING_DIR/"

echo "=== 5. 极致轻量化精简打包 (依赖树剪枝 + 仅保留必需插件与闭包 DLL) ==="
python3 "$PROJECT_ROOT/.tools/package_windows_lightweight.py" "$SDK_DIR" "$CROSS_DIR" "$STAGING_DIR"

echo "=== 6. 同步依赖锁定清单、文档、许可证与启动脚本 ==="
cat << 'EOF' > "$STAGING_DIR/GoDeck.cmd"
@echo off
cd /d "%~dp0"
set "PATH=%~dp0;%PATH%"
start "" "%~dp0GoDeck.exe" %*
EOF
chmod +x "$STAGING_DIR/GoDeck.cmd"
cp -p "$STAGING_DIR/GoDeck.cmd" "$STAGING_DIR/GoDeck.bat"
if [ -f "$PROJECT_ROOT/GoDeck.vbs" ]; then
    cp -p "$PROJECT_ROOT/GoDeck.vbs" "$STAGING_DIR/GoDeck.vbs"
fi
if [ -f "$PROJECT_ROOT/packaging/godeck.ico" ]; then
    cp -p "$PROJECT_ROOT/packaging/godeck.ico" "$STAGING_DIR/godeck.ico"
fi

if [ -f "$PROJECT_ROOT/.tools/windows-sdk/manifest_all.json" ]; then
    cp -p "$PROJECT_ROOT/.tools/windows-sdk/manifest_all.json" "$STAGING_DIR/DEPENDENCIES.json"
fi

if [ -f "$DIST_DIR/README.md" ]; then
    cp -p "$DIST_DIR/README.md" "$STAGING_DIR/README.md"
elif [ -f "$PROJECT_ROOT/dist/windows-amd64/README.md" ]; then
    cp -p "$PROJECT_ROOT/dist/windows-amd64/README.md" "$STAGING_DIR/README.md"
fi

if [ -d "$DIST_DIR/licenses" ]; then
    cp -rf "$DIST_DIR/licenses/"* "$STAGING_DIR/licenses/" 2>/dev/null || true
fi
if [ -d "$SDK_DIR/share/licenses" ]; then
    cp -rf "$SDK_DIR/share/licenses/"* "$STAGING_DIR/licenses/" 2>/dev/null || true
fi

echo "=== 7. 原子替换并归档既有交付 ==="
assert_safe_dist_path "$DIST_DIR"
if [ -d "$DIST_DIR" ]; then
    BACKUP_DIR="$DIST_PARENT/windows-amd64-backup-$(date +%Y%m%d-%H%M%S)"
    assert_safe_dist_path "$BACKUP_DIR"
    echo "归档既有交付目录至 $BACKUP_DIR"
    if mv "$DIST_DIR" "$BACKUP_DIR" 2>/dev/null; then
        mv "$STAGING_DIR" "$DIST_DIR"
    else
        echo "提示: 目录被 Windows 锁定，执行增量覆盖更新并清理 staging"
        cp -rf "$STAGING_DIR"/* "$DIST_DIR"/
        rm -rf "$STAGING_DIR"
    fi
    python3 "$PROJECT_ROOT/.tools/clean_obsolete_dlls.py" >/dev/null 2>&1 || true
else
    mv "$STAGING_DIR" "$DIST_DIR"
fi

echo "=== GoDeck Windows amd64 构建与自包含打包成功完成: $DIST_DIR ==="
