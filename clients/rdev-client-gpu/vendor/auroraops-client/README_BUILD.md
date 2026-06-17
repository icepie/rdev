# AuroraOps 客户端构建与部署

`new-client` 是 AuroraOps 的 Rust 客户端，负责设备注册、心跳上报、远程终端、远程桌面代理和本机管理页。当前主程序名为 `auroraops-agent`。

## 能力概览

- 常驻服务：Linux 使用 systemd，Windows 使用 Windows Service Control Manager。
- 本机管理页：默认监听 `http://127.0.0.1:18765/`，用于配置服务端地址、设备名、桌面编码选项和服务自启。
- 远程桌面：基于内置 Weylus 协议能力，桌面端口默认随机本地端口，只通过 AuroraOps 服务端入口访问。
- 远程终端：通过 AuroraOps 服务端 TCP 通道转发到客户端终端会话。
- 硬件编码：Linux 支持 VAAPI/NVENC 尝试，Windows 支持 NVENC/MediaFoundation 尝试。

## 本地构建

```bash
cd new-client
cargo build --release --bin auroraops-agent
```

构建产物：

```text
new-client/target/release/auroraops-agent
```

Windows 交叉检查或构建：

```bash
rustup target add x86_64-pc-windows-gnu
cargo check --target x86_64-pc-windows-gnu --bin auroraops-agent
cargo build --release --target x86_64-pc-windows-gnu --bin auroraops-agent
```

Windows 产物：

```text
new-client/target/x86_64-pc-windows-gnu/release/auroraops-agent.exe
```

macOS 构建：

```bash
rustup target add x86_64-apple-darwin aarch64-apple-darwin
cargo build --release --target x86_64-apple-darwin --bin auroraops-agent
cargo build --release --target aarch64-apple-darwin --bin auroraops-agent
```

macOS 产物：

```text
new-client/target/x86_64-apple-darwin/release/auroraops-agent
new-client/target/aarch64-apple-darwin/release/auroraops-agent
```

说明：macOS 依赖 Apple SDK 和系统 frameworks，建议在 macOS 原生环境或 GitHub Actions `macos-latest` runner 上构建，不建议从 Linux 直接交叉编译。

## Linux 矩阵构建

`docker-build-linux.sh` 用多档基础镜像覆盖主流 Linux 和信创系统。本地默认只编当前机器架构；CI 可在对应原生 runner 上分别出 `amd64` 和 `arm64`。

| `--target` | 基础镜像 | glibc | 覆盖系统 |
| --- | --- | --- | --- |
| `ubuntu2004` | `ubuntu:20.04` | 2.31 | Ubuntu 20.04 及以上，X11 优先 |
| `ubuntu2204` | `ubuntu:22.04` | 2.35 | Ubuntu 22.04 及以上，Wayland/PipeWire 优先 |
| `uos-v20` | `debian:11` | 2.31 | 统信 UOS V20 桌面 |
| `kylin-v10-v11` | `ubuntu:20.04` | 2.31 | 麒麟 V10/V11 桌面 |
| `nfschina-desktop` | `debian:11` | 2.31 | 中科方德桌面 |
| `centos7` | `centos:7` | 2.17 | CentOS 7 系列，X11-only |
| `centos8` | `rockylinux:8` | 2.28 | CentOS/RHEL/Rocky/Alma 8 及以上 |

常用命令：

```bash
cd new-client

# 全部目标，当前机器架构
./docker-build-linux.sh

# 只编 UOS V20 + 麒麟 V10/V11 桌面
./docker-build-linux.sh --target uos-v20,kylin-v10-v11

# 老系统优先用 centos7 目标，glibc 要求最低，但不启用 Wayland/PipeWire
./docker-build-linux.sh --target centos7

# 只编 amd64
./docker-build-linux.sh --arch amd64

# 非 arm64 主机模拟编 arm64 时显式开启 QEMU
./docker-build-linux.sh --arch arm64 --use-qemu

# 使用代理
./docker-build-linux.sh --proxy http://127.0.0.1:12333
```

产物位置：

```text
new-client/dist/linux-matrix/<target>-<arch>/
```

每个目录一般包含：

```text
auroraops-agent
auroraops-agent_<version>-<release>_<arch>.deb
auroraops-agent-<version>-<release>.<arch>.rpm
auroraops-agent.ldd.txt
```

## UOS V10 / Kylin V10 构建

如果目标是统信/UOS V10、麒麟 V10，优先使用这个兼容构建入口。默认基础镜像是 `macrosan/kylin:v10-sp3-2403`。

```bash
cd new-client
./docker-build-uos-v10.sh
```

常用参数：

```bash
./docker-build-uos-v10.sh --platform linux/amd64
./docker-build-uos-v10.sh --no-cache
./docker-build-uos-v10.sh --proxy http://127.0.0.1:12333
./docker-build-uos-v10.sh --base debian:buster
```

产物位置：

```text
new-client/dist/uos-v10/auroraops-agent
new-client/dist/uos-v10/auroraops-agent_<version>-<release>_<arch>.deb
new-client/dist/uos-v10/auroraops-agent.ldd.txt
```

说明：远程桌面依赖 X11、DBus、GStreamer、FFmpeg、DRM 等宿主桌面库，这些库不适合完全静态链接。Docker 构建主要解决 glibc 和编译环境兼容问题，目标机器仍需要对应运行库。

兼容性选择建议：

- 桌面系统较新，且需要 Wayland/PipeWire：优先 `ubuntu2204`、`uos-v20`、`kylin-v10-v11`。
- 老系统或不确定 glibc 版本：优先 `centos7`，功能取舍为 X11-only。
- RHEL/Rocky/Alma/CentOS 8+：优先 `centos8`。
- 不建议在新发行版本机直接 release 后分发给老系统，因为二进制会绑定构建机的 glibc 版本。

## Linux 安装与服务

开发环境前台运行：

```bash
./target/release/auroraops-agent --service \
  --config /etc/auroraops/agent-config.json \
  --port 18765
```

安装 systemd 服务可使用仓库脚本或系统包：

```bash
sudo ./install-systemd.sh
sudo systemctl enable --now auroraops-agent.service
sudo systemctl status auroraops-agent.service
```

卸载：

```bash
sudo ./uninstall-systemd.sh
```

Linux 服务默认配置路径：

```text
/etc/auroraops/agent-config.json
```

本机管理页服务管理按钮在 Linux 下会调用：

```text
systemctl enable --now auroraops-agent.service
systemctl disable --now auroraops-agent.service
systemctl restart auroraops-agent.service
```

## Windows 安装与服务

Windows 支持自动 UAC 提权注册服务。普通用户双击或命令行执行服务管理命令时，会弹出管理员权限确认窗口。

安装并启动服务：

```powershell
.\auroraops-agent.exe --install-service
```

停止并卸载服务：

```powershell
.\auroraops-agent.exe --uninstall-service
```

其他服务管理命令：

```powershell
.\auroraops-agent.exe --start-service
.\auroraops-agent.exe --stop-service
.\auroraops-agent.exe --restart-service
```

服务信息：

```text
服务名: auroraops-agent
显示名: AuroraOps 客户端
启动类型: auto
运行账户: LocalSystem
默认配置: C:\ProgramData\AuroraOps\agent-config.json
用户界面配置: %APPDATA%\AuroraOps\config.toml
旧版兼容读取: %APPDATA%\weylus\weylus.toml
默认管理页: http://127.0.0.1:18765/
```

### Windows 会话模型

Windows 服务运行在 Session 0，不能直接稳定捕获当前登录用户桌面。因此 AuroraOps 客户端采用两层进程：

- 服务进程：由 Windows SCM 常驻，负责自启、监控和拉起用户会话代理。
- 会话代理：服务检测当前活动 Console Session 后，通过 `CreateProcessAsUserW` 在该用户桌面会话里启动 `auroraops-agent.exe --session-agent --service ...`。
- 远程终端：使用 `portable-pty`，Windows 走 ConPTY，Linux/macOS 走原生 PTY。
- 远程键盘：Windows 使用 `SendInput` 发送键盘事件，避免 autopilot 字符输入路径 panic。

服务每隔数秒检查活动会话：

- 用户登录后自动拉起 session agent。
- 用户切换会话后终止旧 agent 并拉起新 agent。
- session agent 异常退出后自动重启。
- 停止 Windows 服务时同步终止 session agent。

排查命令：

```powershell
sc query auroraops-agent
sc qc auroraops-agent
Get-Process auroraops-agent
```

正常情况下，用户登录后可能看到两个 `auroraops-agent.exe` 进程：一个是 SCM 服务进程，一个是当前桌面会话中的 `--session-agent` 进程。

### Windows 安装包

GitHub Actions 会产出：

```text
auroraops-agent-windows-x64.exe
AuroraOps-Client-Setup-<version>-x64.exe
```

本地构建 setup.exe 需要安装 NSIS：

```powershell
cargo build --release --bin auroraops-agent
$version = (Select-String -Path Cargo.toml -Pattern '^version = "(.+)"' | Select-Object -First 1).Matches.Groups[1].Value
makensis.exe /DVERSION=$version /DSOURCE_EXE="$PWD\target\release\auroraops-agent.exe" packaging\windows\auroraops-client.nsi
```

setup.exe 默认安装到 `C:\Program Files\AuroraOps\AuroraOps Client\`，创建桌面和开始菜单快捷方式，并可选择注册启动 `auroraops-agent` 系统服务。

## 本机管理页

启动服务后打开：

```text
http://127.0.0.1:18765/
```

可配置项：

- 服务端地址和设备名称。
- 远程桌面绑定地址和本地端口。端口 `0` 表示随机本地端口。
- Linux 桌面能力：Wayland/PipeWire、KMS/DRM、VAAPI、NVENC、登录界面控制。
- Windows 桌面能力：NVENC、MediaFoundation。
- 系统服务启用、禁用和重启。

远程桌面本地端口默认不固定，也不需要直接暴露给公网；管理后台通过服务端通道打开远程桌面。

## 硬件资产采集

客户端优先使用 vendored `fastfetch-sys` 直接链接 fastfetch native detection library，当前在 Linux、Windows 和 macOS 都参与构建。

- Linux：使用预生成 bindgen 绑定，采集主板、BIOS、CPU、内存、GPU、网卡和磁盘；必要时补 `/proc` fallback。
- Windows：构建时启用 Windows fastfetch native detection，并裁剪不适合 agent 静态链接的媒体和 GPU 扩展；如果某些类型没有采到，会自动用 PowerShell CIM 作为 fallback。
- macOS：通过 macOS 原生 runner 构建 native detection library，链接 Apple system frameworks。不同 CPU 架构会通过 `CMAKE_OSX_ARCHITECTURES` 固定 native C 库架构，避免 Rust target 和 CMake 产物架构不一致。

单独验证 `fastfetch-sys`：

```bash
cargo check --manifest-path new-client/vendor/fastfetch-sys/Cargo.toml
cargo check --manifest-path new-client/vendor/fastfetch-sys/Cargo.toml --target x86_64-pc-windows-gnu
```

macOS 验证请在 macOS 环境中运行：

```bash
cargo check --manifest-path new-client/vendor/fastfetch-sys/Cargo.toml --target x86_64-apple-darwin
cargo check --manifest-path new-client/vendor/fastfetch-sys/Cargo.toml --target aarch64-apple-darwin
```

## 验证命令

Linux 主目标：

```bash
cargo check --manifest-path new-client/Cargo.toml --bin auroraops-agent
```

Lite 服务：

```bash
cargo check --manifest-path new-client/Cargo.toml \
  --bin auroraops-agent-service \
  --no-default-features \
  --features agent-service-lite
```

Windows 目标：

```bash
cargo check --manifest-path new-client/Cargo.toml \
  --target x86_64-pc-windows-gnu \
  --bin auroraops-agent
```

macOS 目标：

```bash
cargo check --manifest-path new-client/Cargo.toml \
  --target x86_64-apple-darwin \
  --bin auroraops-agent
cargo check --manifest-path new-client/Cargo.toml \
  --target aarch64-apple-darwin \
  --bin auroraops-agent
```

## 常见问题

### GLIBC 版本不足

```text
./auroraops-agent: /lib/x86_64-linux-gnu/libc.so.6: version `GLIBC_2.xx' not found
```

使用更老的目标镜像重新构建，例如：

```bash
./docker-build-linux.sh --target centos7
```

### Linux 远程输入不可用

确认 `/dev/uinput` 可用：

```bash
ls -l /dev/uinput
sudo modprobe uinput
```

打包安装时会尝试安装 uinput 配置；手动运行时需要确保当前用户或服务有权限访问 `/dev/uinput`。

### KMS/DRM 捕获失败

常见错误：

```text
KMS framebuffer handle unavailable
KMS PRIME mmap failed: Operation not permitted
DRM_IOCTL_MODE_MAP_DUMB
```

处理方向：

- 确认使用正确的 `/dev/dri/card*`。
- 尝试 root 或具备 DRM 权限的服务运行方式。
- 部分驱动不支持当前 KMS fallback 路径，优先使用 X11/PipeWire 捕获。

### Windows 服务已启动但没有桌面画面

检查 session agent 是否被拉起：

```powershell
Get-Process auroraops-agent | Select-Object Id,SessionId,Path
```

如果只有 Session 0 的服务进程，说明当前没有可用的活动登录会话，或 `WTSQueryUserToken/CreateProcessAsUserW` 失败。确认服务账户为 LocalSystem，并查看服务日志或控制台输出。
