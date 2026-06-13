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
| Web UI | 实时查看已连接设备列表 |
| 自动重连 | 客户端断开后自动重连 |
| 指定 Shell | `--shell /bin/bash` 或 `$RDEV_SHELL` |
| 跨平台 | Unix (creack/pty) / Windows (ConPty) / 其他 (pipe) |

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

## 构建

```bash
go build -o rdev-server ./cmd/rdev-server
go build -o rdev-client ./cmd/rdev-client

# 交叉编译
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o rdev-client.exe ./cmd/rdev-client
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o rdev-client-arm64 ./cmd/rdev-client
```

## 目录结构

```
rdev/
├── cmd/
│   ├── rdev-server/main.go     # 服务端入口
│   └── rdev-client/main.go     # 客户端入口
├── internal/
│   ├── protocol/protocol.go    # WebSocket 协议消息定义
│   ├── ptyutil/                # 跨平台 PTY (基于 go-pty)
│   │   └── pty.go              # Unix: creack/pty + SSH modes, Windows: ConPty
│   ├── server/
│   │   ├── server.go           # WebSocket 服务 + 客户端管理
│   │   ├── ssh.go              # SSH 服务 + Session + -L转发
│   │   ├── forward.go          # -R 端口转发处理
│   │   └── static/index.html  # Web UI
│   └── client/
│       └── client.go           # 客户端: Shell/Exec/SFTP/转发
└── go.mod
```

## 协议

WebSocket 消息使用 JSON 编码，二进制数据用 base64:

| 消息类型 | 方向 | 说明 |
|----------|------|------|
| register | C→S | 注册设备 ID + 密码 |
| new_session | S→C | 创建 Shell/Exec/SFTP 会话 |
| data | 双向 | stdout 数据 (base64) |
| stderr | 双向 | stderr 数据 (base64) |
| stdin_close | S→C | 远端关闭 stdin (EOF) |
| close | 双向 | 关闭会话 |
| resize | S→C | 终端大小变更 |
| exit_code | C→S | 命令退出码 |
| tcp_connect | S→C | 端口转发: 连接目标 |
| tcp_open | C→S | 端口转发: 连接成功 |
| tcp_fail | C→S | 端口转发: 连接失败 |
| tcp_data | 双向 | 端口转发: TCP 数据 |
| tcp_close | 双向 | 端口转发: 关闭连接 |

## License

MIT
