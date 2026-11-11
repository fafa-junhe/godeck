#!/usr/bin/env python3
import os
import shutil
import sys
from pe_deps import get_pe_imports

SYSTEM_PREFIXES = (
    "api-ms-", "ext-ms-", "kernel32", "user32", "gdi32", "shell32", "advapi32",
    "ole32", "oleaut32", "ws2_32", "ntdll", "msvcrt", "ucrtbase", "d3d11",
    "dxgi", "d3d9", "d3d12", "d2d1", "mf", "mfplat", "mfreadwrite", "dxva2", "evr",
    "winmm", "setupapi", "shlwapi", "version", "crypt32", "bcrypt", "dnsapi",
    "iphlpapi", "secur32", "imm32", "comctl32", "comdlg32", "dwmapi", "uxtheme"
)

def is_system(name):
    low = name.lower()
    return low.startswith(SYSTEM_PREFIXES)

def main():
    if len(sys.argv) < 4:
        print("Usage: package_windows_lightweight.py <SDK_DIR> <CROSS_DIR> <STAGING_DIR>")
        sys.exit(1)

    sdk_dir = os.path.abspath(sys.argv[1])
    cross_dir = os.path.abspath(sys.argv[2])
    staging_dir = os.path.abspath(sys.argv[3])

    staging_plugins_dir = os.path.join(staging_dir, "gstreamer-1.0")
    sdk_plugins_dir = os.path.join(sdk_dir, "lib", "gstreamer-1.0")
    sdk_bin_dir = os.path.join(sdk_dir, "bin")
    compiler_bin_dir = os.path.join(cross_dir, "x86_64-w64-mingw32", "bin")

    os.makedirs(staging_plugins_dir, exist_ok=True)

    essential_plugins = [
        "libgstapp.dll",
        "libgstcoreelements.dll",
        "libgstvideoconvertscale.dll",
        "libgstvideorate.dll",
        "libgstvideoparsersbad.dll",
        "libgstd3d11.dll",
        "libgstd3d.dll",
        "libgstmediafoundation.dll",
        "libgstx264.dll",
        "libgstopenh264.dll",
        "libgsttypefindfunctions.dll",
        "libgstautodetect.dll",
        "libgstwinscreencap.dll",
    ]

    print("=== 复制必要 GStreamer 插件 ===")
    copied_plugins = []
    for p in essential_plugins:
        src = os.path.join(sdk_plugins_dir, p)
        if not os.path.exists(src):
            # check root dist if already staged
            src = os.path.join("dist", "windows-amd64", "gstreamer-1.0", p)
        dst = os.path.join(staging_plugins_dir, p)
        if os.path.exists(src):
            shutil.copy2(src, dst)
            copied_plugins.append(dst)
            print(f"  + 插件: {p}")
        else:
            print(f"  ! 警告: 未找到插件 {p}")

    # Binaries to trace
    binaries = [
        os.path.join(staging_dir, "GoDeck.exe"),
        os.path.join(staging_dir, "gst-plugin-scanner.exe"),
    ] + copied_plugins

    # Build available DLL index
    available = {}
    for d in [sdk_bin_dir, compiler_bin_dir, os.path.join("dist", "windows-amd64")]:
        if os.path.exists(d):
            for f in os.listdir(d):
                if f.lower().endswith(".dll") and f.lower() not in available:
                    available[f.lower()] = os.path.join(d, f)

    print(f"扫描到候选 DLL 总数: {len(available)}")

    # BFS trace
    needed = {}
    queue = [b for b in binaries if os.path.exists(b)]
    missing = set()

    while queue:
        curr = queue.pop(0)
        imports = get_pe_imports(curr)
        for imp in imports:
            imp_lower = imp.lower()
            if is_system(imp_lower):
                continue
            if imp_lower in available:
                if imp_lower not in needed:
                    src_path = available[imp_lower]
                    needed[imp_lower] = src_path
                    queue.append(src_path)
            else:
                missing.add(imp)

    print(f"\n=== 解析到精准依赖闭包: 共 {len(needed)} 个 DLL ===")
    total_size = 0
    for name_lower, src_path in sorted(needed.items()):
        dst_path = os.path.join(staging_dir, os.path.basename(src_path))
        shutil.copy2(src_path, dst_path)
        sz = os.path.getsize(dst_path)
        total_size += sz
        print(f"  -> {sz/1024:7.1f} KB : {os.path.basename(dst_path)}")

    print(f"\n依赖 DLL 总体积: {total_size / (1024*1024):.2f} MB")
    if missing:
        print(f"警告: 系统未知缺失依赖: {missing}")
    else:
        print("闭包完整性校验: 100% 完整，无任何系统外缺失！")

if __name__ == "__main__":
    main()
