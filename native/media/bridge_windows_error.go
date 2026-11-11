//go:build windows && amd64 && !cgo && !godeck_preview

package media

// GoDeck 正式 Windows 原生客户端依赖 CGO (CGO_ENABLED=1) 与 GStreamer/D3D11 媒体桥。
// 如果您在没有 CGO 工具链的环境下仅需预览客户端原生 UI 布局，请添加构建标签：
//     go build -tags godeck_preview .
// 正式构建请参考 dist/windows-amd64/ 说明或使用已打包的完整客户端。

const _ = "GoDeck 构建错误：Windows 正式客户端必须启用 CGO (CGO_ENABLED=1) 且依赖 GStreamer SDK。若仅构建无媒体界面预览，请使用 -tags godeck_preview"()
