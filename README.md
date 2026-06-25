# RDev - Remote Debug over SSH

通过 SSH 远程调试已连接设备。客户端（设备端）连接到服务端后，其他人可通过标准 SSH/SCP/SFTP/Rsync 访问该设备的 Shell 和文件系统，并支持端口转发。

## 架构

```
┌──────────────┐     WebSocket      ┌──────────────┐
│  rdev-client │ ◄──────────────► │  rdev-server │ ◄── SSH Client (调试者)
│  (被调试设备) │    控制通道+数据    │  SSH+Web UI  │
└──────────────┘                   └──────────────┘
```

- **客户端** 运行在被调试设备上，通过 WebSocket 连接服务端
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
| Remote Desktop | 已支持浏览器远程屏幕查看与输入控制 MVP（Linux X11/DRM/fbdev、Windows GDI/DXGI、macOS Quartz/CoreGraphics no-cgo 截屏；输入后端含 XTEST、可选 uinput、Win32、可选 Win8+ Touch Injection、macOS Quartz mouse/keyboard；默认 CGO_ENABLED=0），设计见 `docs/remote-desktop.md` |
| Rust GPU Client | 可选实验版 `clients/rdev-client-gpu`，优先补齐 SSH/session/内置 SFTP/Rsync/TCP/file 基础能力，并提供可选内置 RDev/Weylus 风格 GPU 桌面隧道；Win7 包使用普通 Windows GNU 构建加 PE import patch 和兼容 shim DLL |
| Android / Termux | 支持 Termux 一键运行 Go 兼容版与 Rust no-desktop 版，Android/Bionic 包使用 Android libc DNS；详见 `docs/android-termux.md` |
| Android APK | 独立安卓被控端设计采用 `MediaProjection + MediaCodec` 高性能屏幕流，输入走 Accessibility；详见 `docs/android-apk.md` |
| VNC/RFB Bridge | 服务端可选 `--vnc` 暴露现代 VNC 入口，使用 VeNCrypt Plain 用户名/密码认证，`username=deviceId` 选择设备 |

## 快速开始

### 启动服务端

```bash
# 默认端口: HTTP 8080, SSH 2222
./rdev-server

# 自定义端口
./rdev-server --http :9090 --ssh :2200

# 数据目录 (host key, authorized_keys)
./rdev-server --data /etc/rdev

# 可选 VNC/RFB 入口（只支持带 username/password 的现代 VNC Viewer）
./rdev-server --vnc 127.0.0.1:5900
```

### 启动客户端

```bash
# 基本连接
./rdev-client -s ws://your-server:8080 -i my-device

# 带密码认证
./rdev-client -s ws://your-server:8080 -i my-device -p secret123

# 指定 Shell
./rdev-client -s ws://your-server:8080 -i my-device --shell /bin/bash

# 环境变量
export RDEV_SERVER=ws://your-server:8080
export RDEV_ID=my-device
export RDEV_SHELL=/bin/fish
./rdev-client -s $RDEV_SERVER -i $RDEV_ID

# Termux / Android 被控终端
pkg install -y curl
curl -sL https://rdev.singzer.cn/run.sh | sh -s -- wss://rdev.singzer.cn -p secret123
```

### 自动更新

客户端和服务端默认启用自动更新：启动 1 分钟后开始检查 GitHub latest release，之后每 1 分钟轮询一次。Go 客户端/服务端发现新版本后会下载当前平台/架构对应资产，使用 `github.com/minio/selfupdate` 替换当前二进制并重启进程。Rust GPU 客户端同样支持 `--no-auto-update`、`--auto-update`、`--update-interval` 和 `RDEV_UPDATE_PROXY`，会下载对应 `rdev-client-gpu-*` 归档并替换当前可执行文件；归档内的 DLL/sidecar 仍由安装脚本或服务包装器负责部署。

```bash
# 禁用自动更新
./rdev-server --no-auto-update
./rdev-client -s ws://your-server:8080 --no-auto-update
./rdev-client-gpu -s ws://your-server:8080 --no-auto-update

# 调整检查间隔
./rdev-server --update-interval 10m
RDEV_UPDATE_INTERVAL=10m ./rdev-client -s ws://your-server:8080

# 自定义 GitHub 下载前缀，多个前缀逗号分隔；内置前缀会在直连失败后自动重试
RDEV_UPDATE_PROXY=https://gh-proxy.com/ ./rdev-server

# 标准 HTTP/HTTPS 代理会同时用于自动更新下载和客户端 WebSocket 主连接
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

## Windows 兼容性

- Windows 10/11 的实验 Rust 客户端优先使用 `portable-pty`/ConPTY，失败后退回 pipe shell。
- Windows 7/8/8.1 的 Go `run.ps1` 会自动下载 WinPTY 运行时，并通过同一套 GitHub 代理前缀重试。
- Rust `rdev-client-gpu` 的 Win7 包会自动探测并打包 `winpty.dll`/`winpty-agent.exe`；运行时也会从 `RDEV_WINPTY_DIR`、程序目录和 `PATH` 探测 WinPTY。
- Win7/Win8 上 Rust PTY 优先 WinPTY，失败后退回 pipe shell；Win10/Win11 上优先 ConPTY，失败后退回 pipe shell。
- Win7 运行 Go 客户端请使用 XTLS `go-win7` 工具链构建，例如 `GO_WIN7=/path/to/go-win7/bin/go make win7-go-client win7-service-wrapper`。
- Windows 服务不要直接托管客户端 EXE；使用 `rdev-service-wrapper.exe`，并在配置中设置 `interactive: true`，让服务在活动登录用户桌面启动客户端，避免桌面截图出现 Session 0 的 `Access is denied`。
- GPU 桌面采用 RDev/Weylus 风格：`rdev-client-gpu` 可通过 `embedded-rdev-desktop` feature 内置启动 vendored RDev 桌面 HTTP/WS 服务并连接 `/gpu-desktop-tunnel`，服务端通过 `/gpu-desktop/<device>/` 代理浏览器流量；服务端只做鉴权和隧道转发。

服务 wrapper 配置示例：

```json
{
  "workDir": "C:\\Windows\\Temp\\rdev-services\\go",
  "log": "C:\\Windows\\Temp\\rdev-services\\go-service.log",
  "interactive": true,
  "command": ["C:\\Windows\\Temp\\rdev-services\\go\\rdev-client.exe", "--server", "wss://rdev.singzer.cn", "--id", "win7-go-svc", "--password", "123", "--no-auto-update"]
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
