# RDev - Remote Debug over SSH

通过 SSH 远程调试已连接设备。客户端（设备端）连接到服务端后，其他人可通过标准 SSH/SCP/SFTP 访问该设备的 Shell 和文件系统，并支持端口转发。

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
| Remote Desktop | 已支持浏览器远程屏幕查看 MVP（Linux X11 / Windows GDI，默认 CGO_ENABLED=0），设计与后续输入控制见 `docs/remote-desktop.md` |

## 快速开始

### 启动服务端

```bash
# 默认端口: HTTP 8080, SSH 2222
./rdev-server

# 自定义端口
./rdev-server --http :9090 --ssh :2200

# 数据目录 (host key, authorized_keys)
./rdev-server --data /etc/rdev
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
```

### 自动更新

客户端和服务端默认启用自动更新：启动 1 分钟后开始检查 GitHub latest release，之后每 1 分钟轮询一次。发现新版本后会下载当前平台/架构对应资产，使用 `github.com/minio/selfupdate` 替换当前二进制并重启进程。

```bash
# 禁用自动更新
./rdev-server --no-auto-update
./rdev-client -s ws://your-server:8080 --no-auto-update

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

- Windows 10 1809+ 使用系统 ConPTY，不需要额外组件。
- Windows 7/8/8.1 的 `run.ps1` 会自动下载 WinPTY 运行时，并通过同一套 GitHub 代理前缀重试。
- WinPTY 文件默认放在 `%TEMP%\rdev-winpty`，客户端也可通过 `RDEV_WINPTY_DIR` 指定 `winpty.dll` 和 `winpty-agent.exe` 所在目录。
- 如果 WinPTY 不可用，客户端会退回 pipe shell，普通命令仍可执行，但完整交互体验会弱一些。

## 构建

```bash
# 标准构建
make build

# 交叉编译 (无 CGO)
make cross

# 手动构建
CGO_ENABLED=0 go build -o rdev-server ./cmd/rdev-server

# 交叉编译客户端
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o rdev-client.exe ./cmd/rdev-client
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o rdev-client-arm64 ./cmd/rdev-client
```
