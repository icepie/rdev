# RDev - Remote Debug over SSH

通过 SSH 远程调试已连接设备。客户端（设备端）连接到服务端后，其他人可通过标准 SSH/SCP/SFTP/Rsync 访问该设备的 Shell 和文件系统，并支持端口转发。

## 架构

```
┌──────────────┐  TCP/KCP/WS 通道   ┌──────────────┐
│  rdev-client │ ◄──────────────► │  rdev-server │ ◄── SSH Client (调试者)
│  (被调试设备) │    控制通道+数据    │  SSH+Web UI  │
└──────────────┘                   └──────────────┘
```

- **客户端** 运行在被调试设备上，通过 TCP、KCP/UDP 或 WebSocket 连接服务端；`--server` 支持逗号分隔的 fallback 地址列表
- **服务端** 运行在公网可达的机器上，暴露 SSH 端口和 Web UI
- **调试者** 使用标准 SSH/SCP/SFTP 连接服务端，用户名=设备ID

## 功能

| 功能 | 说明 |
|------|------|
| SSH Shell | 交互式终端，支持 PTY (跨平台) |
| SSH Exec | 远程命令执行 |
| SCP 上传/下载 | 通过 SFTP 子系统 |
| SFTP | 完整的 SFTP 文件操作 |
| Rsync | 通过 SSH exec 运行设备端 `rsync --server`，支持增量同步 |
| 公钥免密 | `~/.rdev/authorized_keys` 加入公钥即可 |
| 密码认证 | 客户端启动时指定密码 |
| -L 端口转发 | 访问设备侧网络的服务 |
| -R 端口转发 | 暴露调试者本地服务到服务端 |
| Web UI | 实时查看已连接设备列表，支持 Web Terminal/Sessions 内联图片预览 (Sixel / iTerm2 inline image) |
| 自动重连 | 客户端断开后自动重连 |
| 自动更新 | 客户端/服务端默认每 1 分钟检查 GitHub Release，支持内置代理和 `--no-auto-update` 禁用 |
| 指定 Shell | `--shell /bin/bash` 或 `$RDEV_SHELL` |
| 跨平台 | Unix (creack/pty) / Windows (ConPty) / 其他 (pipe) |
| Terminal Modes | SSH pty-req modes 完整转发 (ECHO, ONLCR, etc.) |
| Remote Desktop | 已支持浏览器远程屏幕查看与输入控制 MVP（Linux X11/DRM/fbdev、Windows GDI/DXGI、macOS Quartz/CoreGraphics no-cgo 截屏；输入后端含 XTEST、可选 uinput、Win32、可选 Win8+ Touch Injection、macOS Quartz mouse/keyboard；默认 CGO_ENABLED=0）。设计见 `docs/remote-desktop.md` |
| 客户端版本 | 兼容版 `rdev-client-*`（Go，默认，`CGO_ENABLED=0`，纯 Go H.264 + MJPEG 回退）和性能版 `rdev-client-gpu-*`（Rust，GPU/软件 H.264）。两者均可通过 `run.sh`/`run.ps1` 的 `--client go|rs` 一键启动 |
| Rust GPU Client | 可选实验版 `clients/rdev-client-gpu`，优先补齐 SSH/session/内置 SFTP/Rsync/TCP/file 基础能力，并提供可选内置 RDev/Weylus 风格 GPU 桌面隧道；Win7 包使用普通 Windows GNU 构建加 PE import patch 和兼容 shim DLL |
| Android / Termux | 支持 Termux 一键运行 Go 兼容版与 Rust no-desktop 版，Android/Bionic 包使用 Android libc DNS；详见 `docs/android-termux.md` |
| Android APK | Android 7.0+ 独立被控端，`MediaProjection + MediaCodec` 屏幕流和 Accessibility 输入；单 APK 内嵌真实 AutoJs6 6.8.0 / OpenCV，Agent 可通过 `autojs` 执行设备端脚本；详见 `docs/android-apk.md` |
| VNC/RFB Bridge | 服务端可选 `--vnc` 暴露现代 VNC 入口，使用 VeNCrypt Plain 用户名/密码认证，`username=deviceId` 选择设备 |
| Web Agent | 浏览器内 LLM Agent（`/agent?device=<id>`），通过 OpenAI 兼容接口驱动；除设备侧文件、Shell 与桌面工具外，支持受限 JavaScript 编排多个 Agent 工具及服务端网页搜索/抓取 |

## 快速开始

### 启动服务端

```bash
# 默认端口: HTTP/WS 8080, TCP 8081, KCP/UDP 8082, SSH 2222
./rdev-server

# 自定义端口
./rdev-server --http :9090 --tcp :9091 --kcp :9092 --ssh :2200

# 数据目录 (host key, authorized_keys)
./rdev-server --data /etc/rdev

# 可选 VNC/RFB 入口（只支持带 username/password 的现代 VNC Viewer）
./rdev-server --vnc 127.0.0.1:5900

# 可选桌面、文件和 Web Terminal 的 WebRTC 直连（默认关闭；也可设置 RDEV_WEBRTC=true）
./rdev-server --webrtc

# 可选 Web Agent（OpenAI 兼容接口；配置后 Web UI 出现 Agent 入口）
./rdev-server --agent-api-key sk-... --agent-base-url https://api.openai.com/v1 --agent-model gpt-4o-mini
# 也可用环境变量：RDEV_AGENT_API_KEY / RDEV_AGENT_BASE_URL / RDEV_AGENT_MODEL / RDEV_AGENT_MAX_TURNS

# 多提供商多模型（omp 格式配置文件；页面顶部出现模型切换下拉框，切换保留对话历史）
./rdev-server --agent-config /etc/rdev/model-config.json   # 或 RDEV_AGENT_CONFIG

# 会话历史持久化：每个会话一个 JSONL 文件 <data>/agent/sessions/<device>/<thread>.jsonl，
# 会话与正在执行的轮次都在服务端：关闭/刷新页面不中断执行，重新打开自动恢复并继续观看；
# 默认 30 天未更新自动清理（启动时 + 每小时扫描）
./rdev-server --agent-config ... --agent-session-ttl 720h   # 或 RDEV_AGENT_SESSION_TTL；0 = 关闭持久化
```

桌面 WebRTC 需服务端显式启用、客户端声明 `desktop.rtc: true` 且浏览器支持；浏览器先查询鉴权后的 `/api/desktop/rtc`，关闭或查询失败时不会创建 PeerConnection/信令连接。视频可通过 `rdev-desktop` DataChannel 直连，人工输入和剪贴板保留服务端 WebSocket 以维持鉴权及 Agent 接管仲裁；直连失败自动保留/恢复 WebSocket 视频，不反复立即重协商。当前使用公共 STUN、无 TURN，网络不允许直连时走回退路径。详见 [远程桌面说明](docs/remote-desktop.md)。

文件页与 Agent 文件面板同样支持 WebRTC：服务端启用 `--webrtc`、设备声明 `fileRTC` 且浏览器支持时，上传/下载文件数据走可靠有序的 `rdev-file` DataChannel；目录操作、鉴权、ICE 信令与传输控制仍走服务端。Go 和 Rust 客户端均支持，无需启用远程桌面。健康直连不通过服务端中继文件数据；协商失败或传输中断时取消旧任务，以新任务从已确认的上传偏移/已接收的下载偏移切回中继，不在同一任务内混用两条数据通道。关闭 WebRTC、旧客户端或不支持直连的网络继续使用中继；当前使用 STUN，无 TURN。暂停、取消、关闭面板及控制连接断开会清理对应直连。

Web Terminal 连接支持 `terminalRTC` 的 Rust GPU 客户端时，也可使用可靠有序的 `rdev-terminal` DataChannel 直接传输 PTY 输入/输出；无需启动远程桌面。服务端须启用 `--webrtc`，鉴权、窗口尺寸、退出码与信令仍走服务端。协商失败使用普通中继；已建立的直连断开时，经设备确认后切回**同一个 PTY**，只发送尚未提交的输入，不重放可能已执行的命令，并明确提示在途字节可能丢失。输入排队有界，恢复超时会报错并断开，不静默丢弃后继续。会话监看接入会将该终端切回中继以恢复广播；直连期间的历史输出不保存在服务端。旧客户端、关闭 WebRTC 或不支持 WebRTC 的浏览器继续走中继。ICE 配置沿用 `--webrtc-ice-servers`，包括 TURN 或 `[]` host-only；主控制连接断开会关闭对应 PTY 和直连资源。

`model-config.json` 示例（容忍 `opencode_npm` 等多余字段，`api_key` 支持 `${ENV_VAR}`）：

```json
{
  "version": 1,
  "default_model": "deepseek/deepseek-chat",
  "providers": [
    {
      "id": "deepseek",
      "name": "DeepSeek",
      "base_url": "https://api.deepseek.com/v1",
      "api_key": "${DEEPSEEK_API_KEY}",
      "models": [
        {"id": "deepseek-chat", "name": "DeepSeek Chat"},
        {"id": "deepseek-reasoner", "name": "DeepSeek Reasoner", "reasoning": true}
      ]
    }
  ]
}
```

提供方或单个模型可设置 `"api": "openai-responses"` 使用 `/responses`，模型级设置优先。省略 `api` 或设置 `"openai-completions"` 沿用 `/chat/completions`；未知值会报配置错误。`base_url` 填接口根路径（通常以 `/v1` 结尾），不包含 `/responses`。保持供应商实际支持的模型 ID 不变。

Responses 使用 SSE 和 `store: false`，支持函数工具、图片输入、推理摘要及 usage；同一提供方/地址/模型的原始输出条目随历史保存，用于加密推理与助手 phase 的续轮。切换提供方或模型后退回普通消息/工具历史，不复用原始条目。当前不包含 Responses WebSocket、供应商托管工具或完整 OMP RPC。

Anthropic 原生 Messages 可设置 `"api": "anthropic-messages"`，使用 `/messages`、`x-api-key` 和 `anthropic-version: 2023-06-01`。`base_url` 同样填含 `/v1` 的接口根路径；不使用 Claude 登录/OAuth 凭证。支持流式文本、工具调用/结果、发送图片与工具返回图片、思考签名和 usage（包含缓存输入）。

模型级 `maxTokens` 是输出上限，默认 8192；应按所用模型能力配置。开启可调思考时，同时配置 `"reasoning": true` 和明确的 `thinking.mode`，不根据模型名推测：

- `"thinking": {"mode": "anthropic-adaptive"}`：使用 adaptive 与 low/medium/high effort；界面只提供低、中、高三档；旧客户端或旧偏好的 off 映射为 low，因为此类模型不能关闭思考。
- `"thinking": {"mode": "anthropic-budget"}`：low/medium/high 默认预算为 1024/4096/8192，最多占用 `maxTokens - 1024`，为回答留出空间。可在 thinking 中显式设置 `budgetTokens`；预算需至少 1024 且低于输出上限 1024。off 不发送 thinking 设置。
- 只有确认模型支持 `thinking.display` 时，才设置 `"supportsDisplay": true`，请求可见的 summarized 思考；默认不发送该字段。未配置 thinking 时不主动开启思考参数。

完整的签名/隐藏思考块随本地历史持久化，仅同提供方/地址/模型续轮使用；浏览器只收到可读思考文本。切换模型、失败或取消不会回放无效的签名块。该适配不包含 Anthropic 托管工具、Bedrock/Vertex、OAuth 或服务端压缩 API；`/compact` 仍使用 rdev 的会话摘要实现。

### 启动客户端

连接地址支持逗号分隔的优先级列表：`tcp://host:8081,kcp://host:8082,ws://host:8080`。裸 `host:port` 默认按 TCP 处理，`udp://` 是 KCP over UDP 的别名；Android APK、Go 客户端和 Rust GPU 客户端均支持 TCP/KCP/UDP/WS。


```bash
# 基本连接
./rdev-client -s tcp://your-server:8081,kcp://your-server:8082,ws://your-server:8080 -i my-device

# 带密码认证
./rdev-client -s tcp://your-server:8081,kcp://your-server:8082,ws://your-server:8080 -i my-device -p secret123

# 指定 Shell
./rdev-client -s tcp://your-server:8081,kcp://your-server:8082,ws://your-server:8080 -i my-device --shell /bin/bash

# 环境变量
export RDEV_SERVER=tcp://your-server:8081,kcp://your-server:8082,ws://your-server:8080
export RDEV_ID=my-device
export RDEV_SHELL=/bin/fish
./rdev-client -s $RDEV_SERVER -i $RDEV_ID

# Termux / Android 被控终端
pkg install -y curl
curl -sL https://r.niceaigc.cn/run.sh | sh -s -- wss://r.niceaigc.cn -p secret123

# 一键启动并生成 21 位随机字母数字密码（-p 显式指定时优先生效）
curl -sL https://r.niceaigc.cn/run.sh | sh -s -- wss://r.niceaigc.cn --random-password

# Windows 等价用法
powershell -Command "iwr -useb https://r.niceaigc.cn/run.ps1 | iex; RDev wss://r.niceaigc.cn -RandomPassword"
```

### 自动更新

客户端和服务端默认启用自动更新：启动 1 分钟后开始检查 GitHub latest release，之后每 1 分钟轮询一次。Go 客户端/服务端发现新版本后会下载当前平台/架构对应资产，使用 `github.com/minio/selfupdate` 替换当前二进制并重启进程。Rust GPU 客户端同样支持 `--no-auto-update`、`--auto-update`、`--update-interval` 和 `RDEV_UPDATE_PROXY`，会下载对应 `rdev-client-gpu-*` 归档并替换当前可执行文件；归档内的 DLL/sidecar 仍由安装脚本或服务包装器负责部署。

**更新包签名校验**：下载链路经过第三方 GitHub 代理，客户端内置公钥（Go 与 Rust updater 相同）在替换二进制前验证 ed25519 签名，签名缺失或不匹配会拒绝更新。Go、Rust GPU 与 Android 正式发布 CI 使用 `scripts/sign` 为每个资产生成 `<资产名>.sig`；缺少 `RDEV_UPDATE_SIGNING_KEY` 时直接失败，不上传无签名资产。维护者需要在仓库 Secrets 中配置该密钥（base64 编码的 64 字节 ed25519 私钥，可用 `go run ./scripts/sign -generate` 生成密钥对，并将对应公钥同步到 Go/Rust updater）。分支构建不要求发布密钥。Android 的旁路资产签名不代替 APK 自身的安装签名，现有 debug APK 构建类型不变。**未签名的旧 Release 资产不能被启用验签的客户端自动更新接受**。`RDEV_UPDATE_PROXY` 仅接受 `https://` 前缀（仅本地测试允许 loopback 的 `http://`）。

签名密钥已于 2026-11-15 轮换；Go/Rust 内置新公钥的 SHA-256 指纹为 `57bf24ad70845431d49dc9a3519284856378f7b32a02fd69b554bfd4189d32bb`。只信任旧公钥的客户端/服务端需要手动升级一次到采用新公钥的版本，不能通过关闭验签绕过。Actions Secret 不可回读，请通过受控轮换维护，不要将私钥写入源码、日志或聊天。

`run.sh` / `run.ps1` 不要求 OpenSSL 或额外验签器，也不下载或校验 `.sig`；缓存保留原始包，ZIP/tar 包每次重新解压以保留运行依赖。脚本只做非空、错误页面或文件头等基本格式检查，不保证下载或缓存内容的真实性，请仅使用可信脚本与下载源。Win7/PowerShell 2 的 WinHTTP/TLS 回退保持不变。客户端和服务端内置自动更新仍校验 Ed25519 签名，发布资产仍提供 `.sig`。安装行为回归：`node --test scripts/test-runner-downloads.mjs`、`pwsh -NoProfile -File scripts/test-runner-release.ps1`。

```bash
# 禁用自动更新
./rdev-server --no-auto-update
./rdev-client -s tcp://your-server:8081,kcp://your-server:8082,ws://your-server:8080 --no-auto-update
./rdev-client-gpu -s tcp://your-server:8081,kcp://your-server:8082,ws://your-server:8080 --no-auto-update

# 调整检查间隔
./rdev-server --update-interval 10m
RDEV_UPDATE_INTERVAL=10m ./rdev-client -s tcp://your-server:8081,kcp://your-server:8082,ws://your-server:8080

# 自定义 GitHub 下载前缀，多个前缀逗号分隔；内置前缀会在直连失败后自动重试
RDEV_UPDATE_PROXY=https://gh-proxy.com/ ./rdev-server

# 标准 HTTP/HTTPS 代理会同时用于自动更新下载和客户端 WebSocket 主连接；TCP/KCP 直连不走 HTTP 代理
HTTPS_PROXY=http://127.0.0.1:7890 ./rdev-server
HTTPS_PROXY=http://127.0.0.1:7890 ./rdev-client -s wss://your-server.com
NO_PROXY=localhost,127.0.0.1,.lan ./rdev-client -s wss://your-server.com
```

说明：`RDEV_UPDATE_PROXY` 是 GitHub 下载前缀/模板，不是标准 HTTP 代理。标准代理请使用 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`；其中 `wss://` 连接使用 `HTTPS_PROXY`，`ws://` 连接使用 `HTTP_PROXY`，支持 `http://user:pass@host:port`。

### 远程连接

```bash
# 密码方式
ssh my-device@your-server -p 2222   # 密码 = 客户端 -p 参数

# 免密方式 (先加公钥到服务端)
cat ~/.ssh/id_ed25519.pub >> ~/.rdev/authorized_keys
ssh my-device@your-server -p 2222   # 无需密码!

# SCP
scp file my-device@your-server:/tmp/ -P 2222

# SFTP
sftp -P 2222 my-device@your-server

# Rsync（设备端需安装 rsync；通过 RDev SSH 执行远端 rsync --server）
rsync -az --delete -e 'ssh -p 2222' ./local-dir/ my-device@your-server:/tmp/remote-dir/
rsync -az --delete -e 'ssh -p 2222' my-device@your-server:/tmp/remote-dir/ ./local-dir/

# VNC/RFB（服务端需加 --vnc 127.0.0.1:5900；VNC Viewer 需支持 VeNCrypt Plain username/password）
vncviewer 127.0.0.1:5900
# username = my-device；password = 设备密码
# 无设备密码时 password 可留空；若 Viewer 不允许空密码，可填 my-device
# username 为空时，password=my-device 也可连接无密码设备

# 本地端口转发 (访问设备上 80 端口的服务)
ssh -L 8080:localhost:80 my-device@your-server -p 2222

# 远程端口转发 (暴露本地 3000 端口到服务端)
ssh -R 3000:localhost:3000 my-device@your-server -p 2222
```

### 设备身份与撤销

Go、Rust GPU、Android 客户端在首次注册前持久保存随机 `deviceIdentity` 和独立的 `reconnectProof`（各为 64 位小写十六进制）。服务端只保存 proof 的 SHA-256 校验值；公开设备 ID 和 `instanceId` 都不是重连凭证。不同身份申请同名时分配后缀，已有或已撤销身份的名称不会转给新身份。同一有效身份重启后恢复原名称并替换旧连接。

Go/Rust 默认使用系统配置目录下的 `rdev/identity-*.json`：Linux `$XDG_CONFIG_HOME/rdev` 或 `~/.config/rdev`，macOS `~/Library/Application Support/rdev`，Windows `%APPDATA%/rdev`；`RDEV_IDENTITY_DIR` 可覆盖目录。凭证按服务端入口和配置设备 ID 隔离；文件先私密写入并同步，再原子发布且不覆盖并发进程已保存的身份。Android 使用应用私有 SharedPreferences 的同步提交，提交失败不注册。不要复制凭证到其他设备、手改 proof，或将凭证加入版本库；丢失/损坏时应恢复原文件，删除后重新注册不能恢复旧名称和历史。

Go/Rust 未指定 `--id` 或 `RDEV_ID` 时，默认 ID 为 `rdev-` 加 12 位十六进制字符，来源于同一服务端入口及身份目录下持久保存的默认身份，重启不会重新随机命名；显式 ID 的凭证隔离规则不变。默认身份文件损坏或不可写时启动失败，不生成临时替代身份。升级前使用默认主机名的设备若要保留原身份，应显式传入原 ID（如 `--id 原主机名`），并保留原身份目录；直接采用新的短 ID 会注册为新身份。

设备显示名与稳定 ID 分开：Go/Rust 默认显示主机名，可用 `--display-name '办公室电脑'` 或 `RDEV_DISPLAY_NAME` 覆盖（命令行优先）；Android 在连接设置中单独填写显示名，留空使用系统设备名或机型。修改显示名不更换身份凭据，也不改变 SSH 用户名、连接地址或历史归属；页面在名称与 ID 不同时同时显示两者，旧客户端回退显示 ID。Android 连接链接可携带 `displayName`（别名 `name`），仍需保留原 `id`。

服务端配置持久数据目录后，将注册、替换、重命名和撤销写入 `identity-registry.json`；未配置目录时重启会丢失注册与撤销记录。存储错误拒绝新的身份准入；撤销写盘失败会断开当前设备并返回错误，但重启后的撤销状态必须在修复持久化后确认。`GET /api/identity` 提供不含凭证的清单/审计，`POST /api/identity/rename`（JSON `deviceIdentity`、`newClientId`）修改设备 ID；在线设备会断开并按新 ID 自动重连。`POST /api/identity/revoke`（JSON `deviceIdentity`）永久撤销并断开控制连接和桌面隧道。

例如：`curl -X POST -H "X-RDev-Token: $RDEV_ADMIN_TOKEN" -H 'Content-Type: application/json' -d '{"deviceIdentity":"<64位身份>","newClientId":"rdev-office"}' https://server.example/api/identity/rename`。目标 ID 必须符合 `[A-Za-z0-9._-]{1,64}` 且不能与其他身份重复，包括已撤销身份保留的 ID。

管理 API 使用服务端 admin token；未设置时管理 API（包括重命名和撤销）不受 token 保护。admin token 不控制设备首次注册，首次注册仍是开放的 first-claim 模式；设备密码控制访问设备，不替代身份 proof。跨不可信网络必须使用 WSS 或可信加密通道，TCP/KCP/WS 本身不保护传输中的 proof；反向代理应避免记录 GPU 隧道 URL 的查询参数。C++ 客户端是独立 GameStream 查看器，使用 Sunshine/Apollo 证书配对，不参与 rdev 设备注册。

### 运行健康与指标

服务端通过 `GET /healthz` 提供不带运行信息的公开存活检查；`GET /readyz`、`GET /api/health`、`GET /metrics` 使用管理 API 鉴权（`X-RDev-Token`）。已知历史/身份/定时任务存储故障或调度器停止时，readiness 返回 503，存活检查仍返回 200。

指标采用 Prometheus 文本格式；失败计数仅分 device/transport，进程重启后归零。健康查询不启动懒加载子系统、不扫描磁盘；正常状态仅代表已初始化子系统和已访问历史没有已知故障，不是全盘完整性保证。历史故障应通过 Agent 的显式恢复操作保留原件后恢复；调度器存储故障需先修复存储再重启，不能删除 manifest 绕过。


### 代理端口（HTTP + SOCKS5）

服务端可加 `--proxy`（或环境变量 `RDEV_PROXY_ADDR`）开一个统一代理入口，同一端口自动识别 HTTP 代理与 SOCKS5（按首字节嗅探），通过代理认证用户名选择出口设备：

```bash
./rdev-server --proxy 11080
```

认证复用设备注册密码，两种等价格式：

- 组合式（推荐）：用户名 `设备ID@设备密码`，密码任意值（首个 `@` 分割，设备 ID 本身不含 `@`）
- 标准式：用户名 `设备ID`，密码 `设备密码`
- 无密码设备：用户名必须是设备 ID，密码任意

```bash
# HTTP 代理（CONNECT 与明文 HTTP 绝对 URI 都支持）
curl -x http://my-device:devpass@your-server:11080 https://example.com

# SOCKS5（仅 CONNECT；域名与 IPv4/IPv6 均可）
curl -x socks5://my-device:devpass@your-server:11080 https://example.com

# 组合式 设备ID@密码：URL 中用户名的 @ 需编码为 %40（curl/浏览器均如此）
curl -x 'http://my-device%40devpass:x@your-server:11080' http://example.com
```

性能：每条代理连接由设备回拨一条专用 WebSocket 承载（原始字节透传，连接间完全并行）；旧版客户端自动回退到控制连接组帧路径，协议向后兼容。明文 HTTP 代理为每请求一连接（强制 `Connection: close`）；HTTPS 走 CONNECT 纯透传不受影响。

当服务端 HTTP 端口在反向代理（如 Caddy/Nginx）之后、自动探测的内网地址设备不可达时，用 `--tunnel-base`（或 `RDEV_TUNNEL_BASE`）显式指定回拨入口，例如 `--tunnel-base wss://r.example.com`；经 WebSocket 主连接注册的设备不受影响（始终复用主连接入口）。

安装脚本默认使用当前请求的 `Host` 生成服务地址；服务端位于内网、隧道或反向代理后时，不应依赖这个值。用 `--public-server`（或 `RDEV_PUBLIC_SERVER`）显式指定安装脚本写入客户端的逗号分隔地址，例如 `--public-server wss://r.example.com`；该配置优先于请求 `Host`。

## Web UI 响应式与连接状态

- 页面按路由加载；Dashboard 不预加载 Agent、终端和桌面的重型模块。
- 移动端沿用 BB 的响应式组件和触屏尺寸：导航抽屉支持 Escape 关闭与焦点恢复，窄屏触屏页头图标按钮为 36px、Agent 输入区操作为 40px；桌面保留紧凑密度。文件表格、批量输出和桌面设置在各自区域滚动，终端和 Agent 保留分屏。
- Agent 侧栏以 768px 为断点（不依赖鼠标或触屏）：固定保留 48px 导航轨，默认会话栏 312px；窄屏使用 `min(100vw - 48px, 312px)` 揭开式侧栏，对话区保持宽度整体平移；右侧面板使用全宽滑入布局。移动侧栏默认收起，展开状态不覆盖桌面偏好；横屏短高度时侧栏整体可滚动，导航和底部信息仍可到达。
- Agent 停止请求等待回执期间不可重复提交；重置清空队列、待办和运行状态。明确的连接前配置错误停止自动重连，修正配置后可手动重连；普通网络中断仍自动重试。
- 批量命令与文件分发同一时间只运行一个操作，连接中断会结束未完成的结果并恢复操作按钮；文件列表点击普通文件名可直接下载。

## Web 终端图片预览

Web Terminal 和 Sessions 支持 Sixel、iTerm2 inline image 与 Kitty graphics inline image，可用于 `yazi`、`chafa`、`img2sixel` 等工具的图片预览。Web Terminal 会设置：

```bash
echo "$TERM $COLORTERM $TERM_PROGRAM $RDEV_IMAGE_PROTOCOLS"
# xterm-256color truecolor RDev sixel,iterm2,kitty
```

可用以下命令快速测试：

```bash
# Sixel：推荐优先测试
chafa -f sixels image.png
img2sixel image.png

# iTerm2 inline image：无需额外工具，测试 1x1 PNG
printf '\033]1337;File=inline=1;width=10px;height=10px:%s\a\n' \
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII='

# Kitty graphics inline image：测试 1x1 PNG
printf '\033_Ga=T,f=100,s=10,v=10;%s\033\\\n' \
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII='
```

`yazi` 可配置为 `sixel`、`iterm2` 或 Kitty inline 预览协议。Kitty graphics 的远端文件路径/临时文件模式不会桥接到浏览器，优先使用 inline 数据模式。

相关自动化测试：

```bash
bun run test:web
go test ./...
```

## Web 前端

Web UI 是 React 19 + Tailwind CSS v4 + Radix UI 单页应用，组件/设计令牌来自 [bb](https://github.com/get-bb/bb)（MIT）：

- 源码 `web/ui/`（页面 `pages/`、bb 组件 `components/`、主题令牌 `styles/app.css`、i18n `lib/dictionaries.ts` 中英双语），入口 `web/app.html`。
- 路由保持原有 URL：`/`、`/terminal`、`/remote-desktop`、`/batch`、`/sessions`、`/files`、`/agent`；`?token=` 管理令牌与 `rdev.devicePassword.<id>` 设备密码 localStorage 契约不变。
- 明暗主题：右上角切换 自动/浅色/深色，持久化在 `rdevTheme`，自动模式跟随系统。
- 构建顺序：`bun run build`（vite → `internal/server/static/`，go:embed）必须在 `go build` 之前。`internal/server/static/` 是被 `.gitignore` 忽略的生成目录，不能作为源码手工编辑；安装器源文件只维护在 `web/public/`，构建会同步 `run.sh`、`run.ps1` 和 `mustreademe.md`。
- 前端构建会先清空并重新生成整个嵌入目录；不要与 `go test`、`go vet` 或 `go build` 并发执行，否则 Go 可能在资源被替换的中间状态读取 `go:embed` 文件并失败。CI 与 `make build` 按前端构建完成后再运行 Go 检查。
- 构建时生成 Brotli/gzip 静态资源；服务端按 `Accept-Encoding` 协商并设置 `Vary`，不在请求中压缩。带构建哈希的资源缓存一年且 `immutable`；HTML、安装脚本和缺失资源不使用不可变缓存。
- Dashboard 不再预加载 Markdown 渲染器及完整扩展图标集；进入相关功能时才加载。
- HTTP 请求头超时 5 秒、keep-alive 空闲超时 60 秒；不设置整次请求的读写时限，以保留长时间文件传输、SSE 和 WebSocket。

## Windows 兼容性

- Windows 10/11 的实验 Rust 客户端优先使用 `portable-pty`/ConPTY，失败后退回 pipe shell。
- Windows 10 build 17763（1809）之前没有 ConPTY；`run.ps1` 会自动下载 WinPTY 后再启动交互终端。
- 原生 32 位 Windows 会自动下载 `rdev-client-windows-386.exe`；32 位 PowerShell 运行在 64 位 Windows 上仍会正确下载其原生 `amd64`/`arm64` 客户端。
- Windows `run.ps1` 未指定 `-Version` 时按所选客户端及架构的精确资产查找最新稳定版本，并将下载地址和缓存固定到同一 tag；不会因仅发布其他客户端而误取仓库全局 `latest`。服务端 `/api/release/latest?asset=<文件名>` 及两个下载端点同样按资产解析，指定版本保持原样、缺少资产时失败。回归：`pwsh -NoProfile -File scripts/test-runner-release.ps1`；加 `-Live` 可查询真实元数据并下载 Windows Go 包检查 PE 文件头（不启动客户端）。
- Windows 7/8/8.1 的 Go `run.ps1` 会自动下载 WinPTY 运行时，并通过同一套 GitHub 代理前缀重试。
- Windows 7 的默认 PowerShell 2.0/.NET 2.0 无法通过 `ServicePointManager` 设置 TLS 1.2。首页 Win7 安装命令使用原生 WinHTTP，安装脚本的版本查询和文件下载也提供 WinHTTP 回退，保留证书校验和 HTTPS。系统仍需支持 TLS 1.2（Windows 7 SP1 及相关更新、有效根证书）；未更新的系统不能仅通过修改脚本获得 TLS 支持。
- Rust `rdev-client-gpu` 的 Win7 包会自动探测并打包 `winpty.dll`/`winpty-agent.exe`；运行时也会从 `RDEV_WINPTY_DIR`、程序目录和 `PATH` 探测 WinPTY。
- Win7/Win8 上 Rust PTY 优先 WinPTY，失败后退回 pipe shell；Win10/Win11 上优先 ConPTY，失败后退回 pipe shell。
- Win7 运行 Go 客户端请使用 XTLS `go-win7` 工具链构建，例如 `GO_WIN7=/path/to/go-win7/bin/go make win7-go-client win7-service-wrapper`。
- Windows 服务不要直接托管客户端 EXE；使用 `rdev-service-wrapper.exe`，并在配置中设置 `interactive: true`，让服务在活动登录用户桌面启动客户端，避免桌面截图出现 Session 0 的 `Access is denied`。
- GPU 桌面采用 RDev/Weylus 风格：`rdev-client-gpu` 可通过 `embedded-rdev-desktop` feature 内置启动 vendored RDev 桌面 HTTP/WS 服务并连接 `/gpu-desktop-tunnel`，服务端通过 `/gpu-desktop/<device>/` 代理浏览器流量；服务端只做鉴权和隧道转发。
- 原生 CUA 工具由 Rust GPU 客户端提供：顶层 `cua` 工具先以 `action=tools` 返回精简目录，再用 `action=tools, tool=<工具名>` 按需读取完整输入 schema，最后以 `action=call` 调用。普通 Go 客户端仅使用截图和坐标输入，不链接 Cua SDK，也不声明 `desktop.nativeCua`。
- 原生 CUA 桥接会跨单次工具调用错误保留同一桌面会话；`set_agent_cursor_enabled` / `set_agent_cursor_motion` 只有在同会话状态读回匹配后才返回成功，等待超过 5 秒会作为调用错误返回。macOS GPU 客户端在进程主线程运行 AppKit cursor overlay loop，Tokio 客户端运行在后台线程。
- Rust 客户端在 Linux 上直接调用 `renameat2` 系统调用，兼容 CentOS 7 的旧 glibc；重命名不会覆盖已存在的目标，不支持该系统调用的内核会返回错误而非降级为非原子操作。

服务 wrapper 配置示例：

```json
{
  "workDir": "C:\\Windows\\Temp\\rdev-services\\go",
  "log": "C:\\Windows\\Temp\\rdev-services\\go-service.log",
  "interactive": true,
  "command": ["C:\\Windows\\Temp\\rdev-services\\go\\rdev-client.exe", "--server", "wss://r.niceaigc.cn", "--id", "win7-go-svc", "--password", "123", "--no-auto-update"]
}
```

## 构建

```bash
# 标准构建
make build

# 可选 Rust GPU 客户端（实验版，不替代默认 Go 客户端）
make rust-client-gpu
make rust-client-gpu-check
make rust-client-gpu-smoke
# Windows 7-compatible Rust package, requires MinGW gcc
make rust-client-gpu-win7-package
# Optional real Win7 E2E smoke; set RDEV_GPU_WIN7_HOST/PASSWORD first
make rust-client-gpu-win7-smoke

# Win7 Go client/service wrapper, requires XTLS go-win7
GO_WIN7=/path/to/go-win7/bin/go make win7-go-client win7-service-wrapper

# 交叉编译 (无 CGO)
make cross

# 手动构建
CGO_ENABLED=0 go build -o rdev-server ./cmd/rdev-server

# 交叉编译客户端
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o rdev-client.exe ./cmd/rdev-client
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o rdev-client-arm64 ./cmd/rdev-client
```

发布标签前应通过 Rust 的格式检查、Clippy、单元测试及 `make rust-client-gpu-smoke`；GPU 发布流程要求这些检查和跨平台构建、Windows 包启动检查全部成功。文件传输冒烟使用必需的 SHA-256 上传摘要，并核对上传完成及下载元数据中的摘要。已发布标签不重写；发布检查修复使用新的补丁版本。

### Go 客户端桌面编码

默认 Go 客户端通过 BSD-2-Clause 的 `go.264` 提供 H.264 Baseline/AVCC，
并保留 MJPEG 通用回退；项目将上游模块替换为
`github.com/icepie/go.264 v1.14.1-rdev.1` 维护分支。Linux amd64/arm64
按 NVENC、VA-API、CPU 顺序自动选择，Windows 使用 Media Foundation 后回退 CPU，
macOS 使用 CPU；全部路径仍为 `CGO_ENABLED=0`，不启动 FFmpeg 等外部进程。
Linux 硬件后端通过系统动态加载器访问驱动；需要 musl/scratch 兼容的静态客户端时使用
`GO264_TAGS='-tags go264_nohwaccel' make client`。需要原生 CUA 时使用 Rust GPU 客户端：

```bash
curl -sL https://your-server/run.sh | sh -s -- wss://your-server --client rs
```

未采用 `github.com/Eyevinn/hi264` 作为远程桌面编码器：该库面向 H.264
位流处理和测试图生成，只支持 16×16/8×8 平色块的 I_16x16 图像及 P_Skip，
不接收任意像素执行通用视频编码或运动估计；直接用于桌面会导致文字不可读并在
动态画面产生不可接受的全 IDR 开销。

### Agent 与界面规范

- [Agent 工作区、上下文压缩、定时任务、后台任务与服务端设备路由](docs/agent-workspace.md)
- [统一 UI 风格与响应式规则](docs/ui-style-guide.md)
