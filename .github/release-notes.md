## 下载与安装

- **macOS（Apple Silicon）**：下载 `flyshadow-bar-v*-darwin-arm64.zip`，解压后将 `FlyShadow Bar.app` 拖入「应用程序」文件夹即可运行（命令行 CLI 可直接使用应用内的 `FlyShadow Bar.app/Contents/MacOS/flyshadow-bar`）。
- **Windows（x64）**：下载 `flyshadow-bar-v*-windows-amd64.zip` 解压后运行 `flyshadow-bar.exe`（或直接下载独立 `.exe`）。
- 首次启动会自动读取官方 `~/.FlyShadow/config.json` 与原生核心库（`libflyshadow_core_lib.dylib` / `flyshadow_core_lib.dll`）及 GeoIP 数据库，无需额外配置。
- 可通过 `checksums.txt` 核验所有压缩包与可执行文件的 SHA-256 校验和。

当前安装包未配置 Apple Developer ID 公证及 Windows Authenticode 签名，首次运行时系统可能提示未知来源；macOS 可在「系统设置 → 隐私与安全性」中点击「仍要打开」或执行 `xattr -dr com.apple.quarantine "/Applications/FlyShadow Bar.app"`。
