# GoDeck

GoDeck 是一个用 Go 语言编写的原生私有远程工作台：同一个客户端既能作为被控端接受来访，也能作为控制端发起连接。正式客户端采用原生图形界面（Gio），媒体采用持续 PipeWire/D3D11 采集、H.264 硬件编解码与低延迟 WebRTC；不使用 Electron、Tauri、Wails 或 WebView 等任何浏览器套壳。

> **构建与验证边界说明**：
> 为向用户交付可供直接查看的原生程序，本项目已在隔离构建环境下完成了必要的代码编写、自包含便携打包与二进制交叉编译（生成 `dist/windows-amd64/` 完整自包含包、根目录 `GoDeck.cmd` / `GoDeck.bat` 启动器、`GoDeck-UI-preview.exe` 无媒体预览版与 `dist/linux-amd64/godeck`）。
> 此前曾对编译出的二进制执行过一次静态符号解析以确认底层媒体管线导出符号。
> **依据用户严格指令，绝未运行程序、未启动 scanner/GoDeck、未执行任何功能/视觉/性能验收测试，亦未运行 `go test`、`go vet`、`gofmt`、静态分析等验证命令。二进制与打包完成仅代表编译链接与静态文件归集完毕，未声称硬件已成功或延迟已达标；按自包含方式打包，实际运行效果交由用户在真实设备上体验。**

---

## 1. 架构与运行模式

- **默认/正式原生客户端（核心推荐）**：根目录（`main.go`）与 `./cmd/godeck` 均统一直接调用原生图形界面（Gio）。Windows 原生产物为 `GoDeck.exe`，Linux 原生产物为 `godeck`。支持 Linux KDE Plasma Wayland（Mesa OpenGL/EGL + PipeWire + FFmpeg VA-API）以及 Windows 10/11（D3D11 + Win32/Media Foundation）。包含原生多标签界面、指纹直接信令与中心 Hub 信令、双向 H.264 流、原生键盘鼠标捕获，以及独立的可靠有序文件传输通道。
- **中心信令服务器（Hub）**：代码位于 `./cmd/godeck-hub`，Linux 产物为 `godeck-hub`。负责单主人账号、设备心跳管理与 WebRTC 握手信令（不转发桌面画面与文件内容）。详见 [docs/HUB.md](docs/HUB.md)。
- **Web 兼容模式（显式隔离）**：代码位于 `./cmd/godeck-compat`，产物为 `godeck-compat` 或 `godeck-compat.exe`。基于浏览器和 HTTP 截图轮询的旧版兼容服务，仅在显式构建并启动 compat 命令时运行，用于无图形环境调试，**正式客户端不加载亦不嵌入 HTML**。

---

## 2. Arch Linux 原生客户端

首要硬件与系统目标为：**Arch Linux + KDE Plasma Wayland + Intel Haswell-ULT 核显（kernel i915 + libva-intel-driver i965 驱动）**。

### 依赖安装（以 Arch Linux 为例）

请在登录的图形用户会话中，由管理员安装所需的构建与运行依赖：

```sh
sudo pacman -S --needed go gcc pkgconf wayland libx11 libxkbcommon libxcursor \
    mesa gstreamer gst-plugins-base gst-plugins-bad gst-plugins-ugly gst-libav \
    gst-plugin-va gst-plugin-pipewire pipewire libva libva-intel-driver ffmpeg \
    xdg-desktop-portal-kde
```

> **注意**：
> 1. Haswell 等早期 Intel 显卡使用 `libva-intel-driver` 提供的 i965 驱动，与 Mesa 原生 OpenGL/EGL 共存。不需要全局替换或修改系统 Mesa 驱动。
> 2. 由于现代 GStreamer 1.24+ 的 `vah264enc` 插件已对 i965 编码设置阻止列表，GoDeck 在同进程内集成了基于 FFmpeg `libavcodec` 的 `h264_vaapi` 硬件编码器与 VA-API 硬件解码器，直接通过 PipeWire 获取 NV12 帧进行高效硬件编解码。
> 3. Gio 图形界面编译时**必须使用 `-tags novulkan` 标签**，界面完全运行于 Mesa 原生 OpenGL/EGL 之上，不要求显卡支持 Vulkan，也不使用 Zink。

### 编译与运行

在项目根目录下执行：

```sh
CGO_ENABLED=1 go build -tags novulkan -o godeck ./cmd/godeck
./godeck
```

---

## 3. Windows 原生客户端

面向 Windows 10/11 x86-64。使用 D3D11 进行主显示器持续采集，支持 `videoscale` 与 `videorate` 目标尺寸缩放与帧率约束，编码优先尝试 Media Foundation 硬件编码（`mfh264enc`），解码尝试 Direct3D11 硬件解码（`d3d11h264dec`），并在驱动或硬件不支持时明确回退至软件编解码（`x264enc` / `avdec_h264`）。

### 交付版本说明

1. **正式完整自包含客户端（推荐使用）**：
   - 路径：`dist/windows-amd64/GoDeck.exe`（在项目根目录下可直接双击 `GoDeck.cmd`、`GoDeck.bat` 或静默启动器 `GoDeck.vbs` 快速启动）。
   - 该版本为 `CGO_ENABLED=1` 编译的完整客户端，采用纯 GUI 子系统（无控制台黑框弹出），内嵌多分辨率应用图标。
   - **特别提醒**：本程序按自包含方式打包，**若需分发或移动，必须完整复制整个 `dist/windows-amd64` 目录，绝对不能只单独拷贝 `GoDeck.exe`**。
   - 绿色包总大小仅约 53.8 MB，内置经依赖闭包精准裁剪的 27 个核心运行时 DLL，以及 `gstreamer-1.0/` 目录下 13 个核心插件，编码优先采用 MediaFoundation / x264，解码采用 D3D11 硬件加速并以轻量级 Cisco OpenH264 (887KB) 兜底。
   - 包含自动化可重用构建脚本 `.tools/build_windows_dist.sh`，采用官方 MinGW-w64 GCC 工具链构建，彻底规避跨编译器入口符号异常。
   - 启动时会自动加载同级运行库、插件与扫描器，并将缓存置于当前用户可写缓存目录（`%LocalAppData%`），**无需在系统安装 GStreamer 或修改全局 PATH 环境变量，运行效果待用户体验**。
2. **无媒体 UI 预览版（仅供查看界面布局）**：
   - 路径：根目录下 `GoDeck-UI-preview.exe`。
   - 该版本采用 `-tags godeck_preview` 编译，不依赖 CGO 工具链和外部 DLL。
   - **局限性**：该程序内部媒体管道为 stub，仅用于在未配置 GStreamer 环境的机器上预览原生深色中文排版。界面顶部会提示预览版警示横幅，且已禁用发起远控连接与开启来访服务按钮，不能作为正式远程客户端使用。
3. **正式构建防误触保护**：
   - `native/media` 模块配置了明确的构建约束：未启用 CGO 且未显式指定 `-tags godeck_preview` 时，`go build` 会直接中断并抛出明确错误提示，杜绝误构建出“假冒正式版”的无媒体空壳程序。

详细的环境变量配置、工具链说明及编译命令请参阅专门文档：[docs/WINDOWS.md](docs/WINDOWS.md)。

---

## 4. 核心功能与操作指南

### A. 双角色首页与入站审批

原生界面采用深石墨（`#141619`）与柔和青柠（`#c3e99b`）配色，中文清晰排版。主界面分为两大核心卡片：
1. **本机（接受来访）**：
   - 显示本机名称、服务监听地址（默认 `0.0.0.0:8766`）、证书 SHA-256 指纹以及当前 30 分钟有效的会话密钥。密钥默认以点号遮蔽，可点击显示或一键复制。
   - **开启/关闭来访**：一键控制是否接客。关闭来访会立即拒绝新的连接并结束当前来访。
   - **传出画质预设**：可在会话前切换“**流畅 720p30**”（目标 1280×720，H.264 level 31，WebRTC profile-level-id `42e01f`）与“**均衡 1080p30**”（目标 1920×1080，H.264 level 40，WebRTC profile-level-id `42e028`）。发送端与控制端根据实际 SDP 能力协商；若对端缺少 Level 4.0 支持，会自动降级为 720p30。画面按被控屏幕比例做有界等比缩放并对齐 NV12 偶数分辨率，元数据取自编码器实际输出。
   - **编解码模式**：支持“自动”、“要求硬件”、“软件”三种模式。只有硬件管线实际产出有效数据且首帧确认后，界面才会显示硬件启用及其实际设备节点（如 `/dev/dri/renderD128`）；回读存在 CPU 内存拷贝，不宣称端到端零拷贝，实际延迟与帧率未作测量。
   - **文件传输默认授权**：独立开关，默认为关闭状态。
2. **主动连接（控制端）**：
   - 支持【局域网直连】与【中心账号 (Hub)】两种连接模式，通过顶部标签快速切换。

### B. 连接握手与安全审批

- **局域网直接连接**：
  1. 输入对方设备的 IP:端口（如 `192.168.1.10:8766`），点击“核对目标设备指纹并连接”。
  2. 控制端拉取对端 HTTPS 证书指纹；用户与对端核对一致后，输入对端提供的临时会话密钥，点击确认。
  3. 被控端弹出审批卡片（限时 60 秒），显示来访者机器名与来源 IP。被控端可自主选择“允许仅查看”或“允许桌面控制”，并可勾选是否“同时授权文件传输”。超时或拒绝均会安全取消。
- **中心 Hub 信令连接**：
  1. 输入 Hub 的 HTTPS 地址、用户名与密码登录（新部署服务可直接输入一次性 Setup 码完成初始化）。
  2. 登录后可点击“绑定本机”，设备私有凭据将保存在本地安全存储（Linux `~/.config/godeck/hub-device.json` 权限 0600；Windows 使用 DPAPI 加密存储于 `hub-device.dat`）。重启程序自动复用已有绑定，无需重复创建设备。
  3. 设备列表支持实时搜索、真实在线/暂停状态显示、重命名与解绑确认。
  4. 点击“通过中心发起连接”，中心会建立 30 秒准备租约；控制端根据 Hub 返回的 ICE 服务器配置完成 Pion 初始化与 Offer 收集后提交；被控端通过心跳和长轮询接收邀请并弹出本地审批（限时 120 秒）；批准后双方直接建立 WebRTC 对等流。

### C. 桌面控制与按键隔离

- 连接建立后，**默认仅允许查看**，以防误触。
- 查看端必须主动点击“启用操作”并且**点击远程画面使其获得焦点**后，键鼠输入才会被捕获并发送。
- **本地退出**：在任何时候按下本地键盘 `Esc` 键，立即退出控制并释放本地焦点，绝不将该 `Esc` 发送给对端。
- **远端 Esc**：工具栏提供独立的“发送远端 Esc”按钮，专用于触发远端系统的 Esc 事件。
- 窗口失焦、切换标签页、画面暂停或主动断开时，输入泵（`InputPump`）会自动发送按键释放指令并清空输入队列；若突发网络中断导致释放报文无法抵达，被控端会在会话结束时尽力释放本地按键，保留故障边界，防止将按键残留泄漏至后续连接。

### D. 原生文件传输（`godeck-files`）

- 会话顶部提供【桌面画面】与【文件传输】切换标签，切换视图不会中断后台的文件传输任务。
- **权限独立**：必须在来访时显式授权文件访问权限；未授权时文件功能完全禁用，只读桌面亦可单独授权文件传输。
- **传输机制**：
  - 基于独立的 WebRTC DataChannel，单文件上限 100 MiB。
  - 采用 16 KiB 二进制分块、512 KiB 在途滑动窗口与 DataChannel 缓冲背压（`BufferedAmount <= 64 KiB`），避免单个 RTT 停顿，限制排队以减少对输入响应的影响，实际延迟未测量。
  - 接收方以累计 ACK 反馈进度，发送方仅以实际确认写入的字节数报告进度条。
- **原生文件选择器**：
  - Linux 下优先调用 KDE FileChooser 桌面门户（D-Bus），并在缺少门户时兼容 `kdialog` / `zenity`。
  - Windows 下调用 Win32 原生 `GetOpenFileNameW` 与 `GetSaveFileNameW`。
- **原子无覆盖提交**：
  - 上传/下载均在共享目录内的私有隐藏临时目录接收完整文件，校验无误后通过同文件系统的硬链接（`os.Link`）提交至最终目标路径。
  - 若目标已存在同名文件或文件系统不支持硬链接，立即安全报错，严禁静默覆盖已有文件。

---

## 5. 中心 Hub 服务操作简述

若需使用中心账号与跨局域网信令（需自备或配置外部 TURN 服务器）：

```sh
# 编译中心 Hub（Linux）
go build -o godeck-hub ./cmd/godeck-hub

# 方式一：加载证书以安全 HTTPS 启动（原生 GUI 客户端默认要求 HTTPS）
./godeck-hub -data ./hub-data -listen 0.0.0.0:8787 -tls-cert ./hub-cert.pem -tls-key ./hub-key.pem

# 方式二：本地开发回环测试（仅限本机调试）
./godeck-hub -data ./hub-data -listen 127.0.0.1:8787
```

首次启动会在控制台输出一次性 Setup 码（15分钟有效）。客户端在 Hub 登录卡片中输入该 Setup 码即可创建主人账号。更多关于证书配置、STUN/TURN 设置及快照备份的说明，请参阅 [docs/HUB.md](docs/HUB.md)。

---

## 6. 当前未实现与后续规划

以下特性在本轮范围中明确**尚未提供**：
- macOS 原生客户端（仅保留 Web 模式）。
- 远程音频传输。
- 剪贴板自动同步。
- 自动检查与在线更新。
- 端到端完全零拷贝回读。
- 多组织与多用户团队权限。
- 发布版安装包（如 Windows 安装版 exe 或 Linux deb/rpm 包）。
