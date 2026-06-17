# AuroraOps 客户端快速开始

## 本地构建

```bash
cd new-client
cargo build --release --bin auroraops-agent
```

产物：

```text
target/release/auroraops-agent
```

Windows 产物：

```bash
rustup target add x86_64-pc-windows-gnu
cargo build --release --target x86_64-pc-windows-gnu --bin auroraops-agent
```

```text
target/x86_64-pc-windows-gnu/release/auroraops-agent.exe
```

macOS 产物：

```bash
rustup target add x86_64-apple-darwin aarch64-apple-darwin
cargo build --release --target x86_64-apple-darwin --bin auroraops-agent
cargo build --release --target aarch64-apple-darwin --bin auroraops-agent
```

```text
target/x86_64-apple-darwin/release/auroraops-agent
target/aarch64-apple-darwin/release/auroraops-agent
```

macOS 需要 Apple SDK 和系统 frameworks，请在 macOS 原生环境或 GitHub Actions `macos-latest` runner 上构建。

## Linux 快速运行

前台运行服务：

```bash
./target/release/auroraops-agent --service \
  --config /etc/auroraops/agent-config.json \
  --port 18765
```

打开本机管理页：

```text
http://127.0.0.1:18765/
```

安装为 systemd 服务：

```bash
sudo ./install-systemd.sh
sudo systemctl enable --now auroraops-agent.service
sudo systemctl status auroraops-agent.service
```

## Windows 快速运行

安装并启动 Windows 服务：

```powershell
.\auroraops-agent.exe --install-service
```

该命令会自动请求 UAC 管理员权限，并注册 Windows 服务：

```text
服务名: auroraops-agent
显示名: AuroraOps 客户端
默认配置: C:\ProgramData\AuroraOps\agent-config.json
用户界面配置: %APPDATA%\AuroraOps\config.toml
本机管理页: http://127.0.0.1:18765/
```

Windows 远程终端使用 `portable-pty` / ConPTY。Windows 安装包由 NSIS 生成，CI 产物包含 `auroraops-agent-windows-x64.exe` 和 `AuroraOps-Client-Setup-<version>-x64.exe`。

卸载：

```powershell
.\auroraops-agent.exe --uninstall-service
```

排查：

```powershell
sc query auroraops-agent
sc qc auroraops-agent
Get-Process auroraops-agent
```

Windows 服务进程运行在 Session 0；登录用户桌面控制由服务自动拉起的 `--session-agent` 进程完成。用户登录后，正常会看到服务进程和 session agent 进程。

## Docker Linux 矩阵构建

```bash
cd new-client
./docker-build-linux.sh
```

只构建指定目标：

```bash
./docker-build-linux.sh --target uos-v20,kylin-v10-v11
```

使用代理：

```bash
./docker-build-linux.sh --proxy http://127.0.0.1:12333
```

产物：

```text
dist/linux-matrix/<target>-<arch>/
```

当前矩阵目标：

| 目标 | 基础镜像 | 兼容侧重点 |
| --- | --- | --- |
| `ubuntu2004` | `ubuntu:20.04` | Ubuntu 20.04+，X11 优先 |
| `ubuntu2204` | `ubuntu:22.04` | Ubuntu 22.04+，Wayland/PipeWire 优先 |
| `uos-v20` | `debian:11` | 统信 UOS V20 桌面 |
| `kylin-v10-v11` | `ubuntu:20.04` | 麒麟 V10/V11 桌面 |
| `nfschina-desktop` | `debian:11` | 中科方德桌面 |
| `centos7` | `centos:7` | 老系统，最低 glibc，X11-only |
| `centos8` | `rockylinux:8` | RHEL/Rocky/Alma/CentOS 8+ |

给老发行版分发时不要直接用新系统本机 release 产物，优先走 Docker 矩阵。`centos7` 目标 glibc 要求最低，但不启用 Wayland/PipeWire。

## 验证

```bash
cargo fmt --manifest-path new-client/Cargo.toml --check
cargo check --manifest-path new-client/Cargo.toml --bin auroraops-agent
cargo check --manifest-path new-client/Cargo.toml --no-default-features --bin auroraops-agent
cargo check --manifest-path new-client/Cargo.toml --no-default-features --bin auroraops-agent-service --features agent-service-lite
cargo check --manifest-path new-client/Cargo.toml --target x86_64-pc-windows-gnu --bin auroraops-agent
cargo check --manifest-path new-client/vendor/fastfetch-sys/Cargo.toml
cargo check --manifest-path new-client/vendor/fastfetch-sys/Cargo.toml --target x86_64-pc-windows-gnu
```

`fastfetch-sys` 已接入 Linux、Windows 和 macOS。Windows 构建内置 fastfetch native detection，并在缺失资产类型时用 PowerShell CIM fallback；macOS 通过 macOS runner 链接 Apple frameworks。

## 常见问题

### Linux 输入不可用

检查 uinput：

```bash
ls -l /dev/uinput
sudo modprobe uinput
```

### Linux GLIBC 版本不足

使用更老的矩阵目标重新构建：

```bash
./docker-build-linux.sh --target centos7
```

### Windows 服务启动后没有桌面画面

确认当前机器已有用户登录，并检查 session agent：

```powershell
Get-Process auroraops-agent | Select-Object Id,SessionId,Path
```

如果只有 Session 0 进程，说明服务没有成功拉起用户会话代理，需要检查服务账户是否为 LocalSystem，以及 `WTSQueryUserToken/CreateProcessAsUserW` 相关错误。
