## 下载与安装

- **macOS（Apple Silicon / M 系列）**：下载 `flyshadow-bar-*-arm64.dmg`，打开后将 `FlyShadow Bar.app` 拖入「应用程序」文件夹即可运行（命令行工具 `fsbar` 可软链至应用内的 `FlyShadow Bar.app/Contents/MacOS/FlyShadow Bar`，例如 `ln -sf "/Applications/FlyShadow Bar.app/Contents/MacOS/FlyShadow Bar" /usr/local/bin/fsbar`）。
- **macOS（Intel / x64）**：下载 `flyshadow-bar-*-amd64.dmg`，打开后拖入「应用程序」文件夹运行。
- **Windows（x64）**：下载 `flyshadow-bar-setup-*-amd64.exe` 安装运行。
- **应用内增量更新**：从正式安装包运行后，在「设置 → 订阅、环境与更新」中支持 **Ed25519 签名验证与二进制增量更新（Delta Update）**，仅下载版本间的差异补丁即可一键更新并重启，大幅节省流量。
- 首次启动会自动读取官方 `~/.FlyShadow/config.json`、对应架构的原生核心库（`libflyshadow_core_lib.dylib` / `flyshadow_core_lib.dll`）及 GeoIP 数据库，无需额外配置。
- 可通过 `checksums.txt` 核验所有安装包、归档与增量补丁的 SHA-256 校验和。

当前安装包未配置 Apple Developer ID 公证及 Windows Authenticode 签名，首次运行时系统可能提示未知来源；macOS 可在「系统设置 → 隐私与安全性」中点击「仍要打开」或执行 `xattr -dr com.apple.quarantine "/Applications/FlyShadow Bar.app"`。
