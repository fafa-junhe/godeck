#!/bin/bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SDK_DIR="$PROJECT_ROOT/.tools/windows-sdk/ucrt64"
export PKG_CONFIG_PATH="$SDK_DIR/lib/pkgconfig"
exec /usr/sbin/pkgconf --define-variable=prefix="$SDK_DIR" "$@"
