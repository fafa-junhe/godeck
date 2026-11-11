import os

dist_dir = os.path.abspath("dist/windows-amd64")
plugin_dir = os.path.join(dist_dir, "gstreamer-1.0")

# 27 needed DLLs
needed_dlls = {
    "libffi-8.dll",
    "libgcc_s_seh-1.dll",
    "libgio-2.0-0.dll",
    "libglib-2.0-0.dll",
    "libgmodule-2.0-0.dll",
    "libgobject-2.0-0.dll",
    "libgstapp-1.0-0.dll",
    "libgstaudio-1.0-0.dll",
    "libgstbase-1.0-0.dll",
    "libgstcodecparsers-1.0-0.dll",
    "libgstcodecs-1.0-0.dll",
    "libgstd3d11-1.0-0.dll",
    "libgstd3dshader-1.0-0.dll",
    "libgstdxva-1.0-0.dll",
    "libgstpbutils-1.0-0.dll",
    "libgstreamer-1.0-0.dll",
    "libgsttag-1.0-0.dll",
    "libgstvideo-1.0-0.dll",
    "libiconv-2.dll",
    "libintl-8.dll",
    "libopenh264-8.dll",
    "liborc-0.4-0.dll",
    "libpcre2-8-0.dll",
    "libstdc++-6.dll",
    "libwinpthread-1.dll",
    "libx264-165.dll",
    "zlib1.dll",
}

# 13 essential plugins
needed_plugins = {
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
}

# 1. Clean root DLLs
removed_root = 0
for f in os.listdir(dist_dir):
    if f.lower().endswith(".dll"):
        if f.lower() not in needed_dlls:
            fp = os.path.join(dist_dir, f)
            try:
                os.remove(fp)
                removed_root += 1
            except Exception as e:
                print(f"Error removing {f}: {e}")

print(f"Cleaned {removed_root} unnecessary root DLLs from dist/windows-amd64")

# 2. Clean plugin DLLs
removed_plugins = 0
if os.path.exists(plugin_dir):
    for f in os.listdir(plugin_dir):
        if f.lower().endswith(".dll"):
            if f.lower() not in needed_plugins:
                fp = os.path.join(plugin_dir, f)
                try:
                    os.remove(fp)
                    removed_plugins += 1
                except Exception as e:
                    print(f"Error removing plugin {f}: {e}")

print(f"Cleaned {removed_plugins} unnecessary GStreamer plugin DLLs")

# Check remaining files and total size
total_size = sum(os.path.getsize(os.path.join(root, f)) for root, dirs, files in os.walk(dist_dir) for f in files)
print(f"\nFinal dist/windows-amd64 directory total size: {total_size / (1024*1024):.2f} MB")
print(f"Final root DLL count: {len([f for f in os.listdir(dist_dir) if f.lower().endswith('.dll')])}")
print(f"Final plugin count: {len([f for f in os.listdir(plugin_dir) if f.lower().endswith('.dll')])}")
