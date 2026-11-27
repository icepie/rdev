# 发布产物签名

Windows 与 macOS 的发布产物必须带操作系统级签名后才允许发布。这里复用
`nicecode-tools` 项目已经在用的签名服务与凭据，不另外申请证书。

## 凭据

在 GitHub 仓库 Secrets 中配置与 `nicecode-tools` 同名的条目：

| Secret | 用途 |
| --- | --- |
| `EVSIGN_LICENSE_KEY` | Windows Authenticode 签名服务（evsign）授权 key |
| `MACOS_CERT_P12_BASE64` | Developer ID Application 证书 `.p12` 的 base64 |
| `MACOS_CERT_PASSWORD` | 上面 `.p12` 的密码 |
| `APPLE_API_KEY_ID` | App Store Connect API key id |
| `APPLE_API_ISSUER` | App Store Connect issuer uuid |
| `APPLE_API_KEY_P8` | `AuthKey_<id>.p8` 文件内容 |

`RDEV_UPDATE_SIGNING_KEY` 是 rdev 自己的升级包签名（`scripts/sign`），与这里
的 OS 签名是两套机制，不能互相替代。

## 流程

签名在标签构建（`refs/tags/v*`）和 `workflow_dispatch` 时执行；PR 与普通
`main` 推送不签名（fork 拿不到 secrets）。发布 job 依赖签名 job，因此**标签
构建在签名缺失或失败时不会发布任何产物**。

| 工作流 | job | 产物 |
| --- | --- | --- |
| `build.yml` | `sign-windows` | `rdev-server/rdev-client`（amd64/386/arm64）Authenticode |
| `build.yml` | `sign-darwin` | `rdev-server/rdev-client`（amd64/arm64）Developer ID + 公证 |
| `rust-client-rs.yml` | `sign-windows` | Rust 客户端 Windows 包（exe + 附带 DLL） |
| `rust-client-rs.yml` | `sign-darwin` | Rust 客户端 macOS 包（主程序 + `Frameworks/*.dylib`） |

签名后的产物以同名 artifact 覆盖上传（`overwrite: true`），发布 job 只处理
签名后的字节，升级包 `.sig` 在签名之后生成。

脚本：

- `scripts/ci/sign-windows.ps1`：递归找出 PE 文件，用 `EVSIGN_CLIENT … -sha256 -t digicert -cdn`
  签名，并要求 `Get-AuthenticodeSignature` 返回 `Valid` + 有时间戳。
- `scripts/ci/apple-signing-setup.sh`：把证书导入一次性 keychain，写入
  `notarytool` 凭据 profile，导出 `APPLE_SIGNING_IDENTITY` 等变量。
- `scripts/ci/sign-darwin.sh`：对目录内所有 Mach-O（含 dylib）用
  `--options runtime --timestamp` 签名并校验，再提交公证；`.app/.dmg/.pkg`
  会额外 `stapler staple`。

## 约束

- **macOS 产物不再 UPX 压缩**：自解压 stub 无法保留有效的 Developer ID 签名与
  公证记录，`build.yml` 对 darwin 显式使用 `none` 压缩策略（体积变大是预期）。
- 裸 Mach-O（rdev 发布的 `rdev-server` / `rdev-client`）无法 staple 票据，公证
  记录由 Apple 在线校验；只有 `.app`、`.dmg`、`.pkg` 支持 stapling。
- 公证需要 Apple 服务可访问；`notarytool --wait` 失败即视为签名失败。
- Windows 的 UPX 压缩保留，签名在压缩后的最终字节上进行。

## 本地校验

Windows：

```powershell
Get-AuthenticodeSignature .\rdev-client-windows-amd64.exe |
  Format-List Status, StatusMessage, SignerCertificate, TimeStamperCertificate
```

macOS：

```bash
codesign --verify --strict --verbose=2 ./rdev-client-darwin-arm64
codesign -dv --verbose=4 ./rdev-client-darwin-arm64     # Authority=Developer ID Application: …
spctl --assess --type execute --verbose=2 ./rdev-client-darwin-arm64
```
