# GoDeck 产品约定

GoDeck 的目标是私有远程工作台：同一个原生桌面客户端同时支持主动连接和被连接，不使用浏览器套壳（禁止 Electron、Tauri、Wails 或 WebView）。根目录 main.go 与 ./cmd/godeck 均作为薄入口直接调用原生桌面窗口（Windows 生成 GoDeck.exe，Linux 生成 godeck）；旧 HTTP/HTML 兼容模式已完整隔离至 ./cmd/godeck-compat，不再作为根目录默认行为。

当前原生客户端已完成双角色界面、指纹直连与中心 Hub 信令、双向 H.264 媒体通道、独立输入控制与原生文件传输通道的接线与实现。当前设计面向单设备直连、受信任局域网或 VPN，以及单机中心 Hub；不提供收费、订阅、假广告、虚构设备或虚构在线状态。界面采用中文、深石墨（#141619）与柔和青柠色（#c3e99b），以清晰的信息层次、明确的状态反馈和严谨的安全隔离为先。

## 一、原生客户端架构与媒体路径

1. **核心图形与平台**：
   - 界面使用 Gio 桌面图形库，Linux 构建必须指定 `-tags novulkan` 标签，界面使用 Mesa 原生 OpenGL/EGL，不要求显卡支持 Vulkan，不使用 Zink。
   - 首要硬件目标为 Arch Linux + KDE Plasma Wayland + Intel Haswell-ULT 核显（i915 驱动与 `libva-intel-driver` i965 驱动）。
   - Windows 原生客户端面向 Windows 10/11 x86-64，采集使用 `d3d11screencapturesrc` 主显示器，输入使用 Win32 `SendInput`。

2. **Haswell H.264 硬件编解码与画质预设**：
   - 鉴于现代 GStreamer 1.24+ 的 `vah264enc` 插件已对 i965 编码设置阻止列表，Linux 采集路径采用 PipeWire 持续紧凑 NV12，优先交由同进程 FFmpeg `h264_vaapi` 硬编码器处理；解码采用同进程 FFmpeg VA-API 解码器直接读出 RGBA 帧传给 Gio（经 CPU 内存拷贝，明确不声称零拷贝）。
   - 硬件状态仅在实际产帧并验证 Annex B SPS/PPS/IDR 后才报告硬件成功，报告真实 DRM 设备节点（如 `/dev/dri/renderD128`），不从驱动名称推测。
   - 传出画质预设支持被控端在会话建立前配置：“流畅 720p30”（目标 1280×720，H.264 level 31，WebRTC profile-level-id `42e01f`）与“均衡 1080p30”（目标 1920×1080，H.264 level 40，WebRTC profile-level-id `42e028`）。发送端结合控制端 Offer SDP 中实际声明的 H.264 能力进行协商；若对端缺少 Level 4.0 能力，自动明确降级为 720p30 Level 3.1 编码，实际分辨率以硬件/编码器实际输出为准，不虚标，也不宣称未经验证的端到端商业画质。

3. **双角色与会话生命周期**：
   - 同一客户端实例同时具备被控服务（监听入站）与主动连接能力。
   - 每一个来访请求均独占一个入站槽位（`ReserveIncoming` / `ReleaseIncoming`）；局域网直连与中心 Hub 共享同一个来访占位与本机审批逻辑，禁止同时各进一条绕过限制。
   - 本机来访授权具有明确的倒计时超时（直连 60 秒，Hub 120 秒）；过期或已结束的请求禁止继续批准。
   - 结束来访仅断开来访会话，退出外连仅关闭观看会话，互不影响；输入队列（`InputPump`）与关闭回调严格绑定到当前活跃会话对象，防止残留事件误伤后续连接。

## 二、中心 Hub 原生信令与设备管理

1. **架构与安全边界**：
   - 中心 Hub 负责身份认证、设备元数据与 WebRTC 握手信令，不承载任何远程画面、输入或文件内容；Hub 绝非媒体中继或自动穿透服务。媒体传输由 Pion WebRTC 负责，复杂 NAT 网络依赖用户显式配置的外部 STUN/TURN。
   - Hub 仅面向单主人账号，凭据使用 Argon2id 哈希；登录密码不持久化，登录 Token 仅驻留内存，设备长期凭据私有保存（Linux 0600 文件，Windows DPAPI 加密存储）。

2. **握手与审批时序**：
   - 控制端发起连接时，首先调用 `CreateSession` 获得 30 秒准备租约及 ICE 服务器配置；控制端用其初始化 Pion 并收集完整 ICE Offer 后，调用 `SubmitOffer` 提交 Offer。
   - 被控端通过长轮询 `WaitInvitations` 接收已具备 Offer 的待处理会话，弹出本机审批窗口供用户勾选授权。
   - 被控端批准后生成 Answer，并在 `AnswerSession` 中明确携带本次会话授权的 `controlAllowed` 与 `filesAllowed` 及实际媒体参数。
   - 控制端轮询获得 `ready` 状态后应用 Answer，建立对等连接。
   - 退出中心账号仅清理 Hub 内存态并断开中心长连接，不删除磁盘上的设备绑定，也不中断正在进行的独立局域网直连。

## 三、原生文件传输协议与安全性

1. **协议规范**：
   - 采用独立、可靠、有序的 WebRTC DataChannel（Label: `godeck-files`），单任务限制最大 100 MiB。
   - 数据采用最大 16 KiB 的二进制分块（`'D' + 32字节任务ID + 8字节BigEndian偏移 + 负载`）。
   - 采用 512 KiB 在途滑动窗口与 DataChannel `BufferedAmount`（<=64 KiB）背压机制，避免每 16 KiB 停顿等待 RTT，同时避免内存无界积压，限制排队以减少对输入响应的影响，实际延迟未测量。
   - 接收方采用累计 ACK 确认机制，发送方以实际确认字节报告真实进度；任务超时、断开或取消时及时清理临时文件。

2. **权限与文件安全**：
   - 仅限单一共享目录树（可含子目录，深度 ≤ 32，单次文件夹操作 ≤ 10000 条目），必须由查看端主动发起列表、上传或下载；被控端绝不能主动向查看端推送未经请求的文件。
   - `filesAllowed` 属于独立审批权限，默认关闭，与桌面控制权限完全解耦；被控端在每次操作时持续核验权限有效性。
   - 路径按 `/` 分隔逐级校验：每一级都须通过文件名规则，严禁 `..`、控制字符、Windows ADS 流或保留名称（CON、NUL、COM1 等）；逐级拒绝符号链接、Junction 与重解析点，传输对象严格限制为普通文件。
   - 提交原子性：私有临时文件在目标目录内完成后，通过同文件系统硬链接（`os.Link`）不覆盖提交；同名时自动另存为 `名称 (n).扩展名` 并告知用户，不支持硬链接时以 `O_EXCL` 原子占位后移动，严禁覆盖已有文件。

3. **原生文件界面**：
   - 独立【文件】页：目录浏览（面包屑/上一级/筛选）、文件与文件夹上传下载、从系统文件管理器拖放上传（Windows `WM_DROPFILES`、Wayland `text/uri-list`，X11 暂不支持），顺序传输队列含逐项进度、取消、全部取消、重试与清除。
   - 拖放支持通过对 `third_party/gioui` 的本地补丁实现（`app/filedrop*.go`），非上游 Gio 功能。
   - 原生文件选择器：Linux 优先使用 KDE FileChooser 桌面门户（D-Bus），Windows 使用原生 `GetOpenFileNameW` / `GetSaveFileNameW`，杜绝单纯依靠文本框手动输入路径。

## 四、剪贴板同步

- 独立、可靠、有序的 DataChannel（Label: `godeck-clipboard`），由查看端发起；被控端仅当本机审批勾选“允许双向剪贴板同步”时接受，否则立即关闭通道。该权限独立于控制与文件权限，默认关闭，无需修改 Hub 协议。
- 仅同步 UTF-8 文本，单条 ≤ 1 MiB，32 KiB 二进制分片（`'C' + 序号 + 分片索引 + 分片数 + 负载`）；连接建立时不推送已有内容，以内容哈希抑制回环。
- Windows 轮询 `GetClipboardSequenceNumber`；Linux Wayland 使用 `wl-clipboard`（data-control 协议），X11 回退 `xclip`。

## 五、当前暂不包含范围

- 尚不支持 macOS 原生客户端（仅保留 Web 模式截图）。
- 尚不支持音频传输、剪贴板图片/文件同步、X11 下的文件拖放、多团队/组织、自动更新、端到端零拷贝。
- 尚无预编译安装版软件包（如 Windows 安装程序或 Linux deb/rpm）。

## 六、构建与验证状态说明

1. **已执行动作边界**：
   - 为交付可供直接查看的原生程序，已在隔离编译环境中完成了必要的代码编写、自包含便携打包与二进制交叉编译：
     - `dist/windows-amd64/` 完整自包含便携客户端（包含基于 MinGW-w64 GCC 编译的 `GoDeck.exe`、77 个运行库 DLL 含 `libgcc_s_seh-1.dll`/`libiconv-2.dll`/`libstdc++-6.dll` 等传递依赖、`libexec/gstreamer-1.0/gst-plugin-scanner.exe` 扫描器及 237 个 GStreamer 插件）；
     - 根目录 `GoDeck.cmd` / `GoDeck.bat` 快捷启动器；
     - 根目录 `GoDeck-UI-preview.exe`（使用 `-tags godeck_preview` 显式编译的无媒体 UI 预览版）；
     - `dist/linux-amd64/godeck` Linux 原生客户端及环境依赖说明；
     - 在非 CGO 环境下正式构建已配置防误构建编译拦截（`bridge_windows_error.go`）。
   - **历史检查记录**：在此前开发阶段中，曾对编译产物执行过一次静态符号解析（检查目标二进制是否包含 GStreamer / D3D11 媒体符号），如实记录该事实。
2. **严禁执行与未验证事项（遵守用户最高约束）**：
   - 绝未运行任何形式的测试命令（如 `go test`、`go vet`、静态分析与代码格式化校验）；
   - 绝未启动 GoDeck 原生客户端、插件扫描器（`gst-plugin-scanner.exe`）、Web 兼容服务或 Hub 服务进行任何功能、视觉或性能验收；
   - 绝未执行 `vainfo`、`gst-inspect`、FFmpeg 测试管线或硬件诊断命令；
   - 绝未修改操作系统驱动、全局注册表或系统环境变量；
   - 未声称硬件已成功启用或延迟、帧率达到特定商业指标；按自包含方式打包，分发时必须完整拷贝整个 `dist/windows-amd64` 文件夹，实际运行效果与交互表现留待用户在真实机器上体验检验。
