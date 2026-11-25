# FlyShadow Bar AI 开发规范

本文件规定 AI 在本仓库的开发、验证、发布与提交行为。面向用户的产品说明与使用方式见 [README.md](README.md)。

## 执行约定

- 项目操作以本文件为主，用户明确要求优先于默认约定；不可覆盖的系统级指令仍须遵守。
- 默认在当前分支（通常为 `main`）和工作目录直接开发与提交，不主动新建分支或开 PR；未获用户明确要求时不要擅自 `git commit` 或 `git push`。
- 保留已有及其他改动，不恢复、不覆盖无关文件；提交使用约定式提交与简体中文（如 `feat: 说明`、`fix: 说明`、`docs: 说明`）。
- 文档职责清晰分离：`README.md` 面向最终用户，只保留产品介绍、截图、用法与基础构建；本文件面向维护者与 AI Agent，记录工程红线、实证约束与自动发布流程，不向 `README.md` 泄露内部发布或开发规范。
- 说明、注释和文档使用简体中文，代码标识符遵循项目既有 Go 命名；文档直接写成最终形态，不保留修改过程痕迹。

## 隐私与安全底线

- 严禁在代码、测试、日志、终端输出、截图、文档或 Git 提交中暴露任何真实节点地址（host）、密码、订阅链接、账号 token、私人分组名或第三方 API Key。
- 涉及对外展示或提交仓库的截图，必须使用完全虚构的脱敏测试配置（模拟节点、规则与连接数据），严禁截取用户真实 `~/.flyshadowbar/config.json` 或 `~/.FlyShadow/config.json` 界面。
- 本地功能验证、代理实测或运行 `FLYSHADOW_LIVE_TEST=1` 时，必须使用临时目录配置（设置 `FLYSHADOW_BAR_DIR` 或 `--config`）与随机/隔离高端口，并设置 `--no-auto-sysproxy`（或 `DisableAutoSysProxy: true`），严禁修改用户真实的 `~/.flyshadowbar/config.json`、`~/.FlyShadow/config.json`、系统代理设置或占用宿主机正在运行的代理端口（如 `7890`）。

## 架构与构建约束

- **全程零 CGO（`CGO_ENABLED=0`）**：所有构建、测试与交叉编译必须保持 `CGO_ENABLED=0`，不得引入 C 源码、CGO 依赖或需要 C 编译器的库；确保可跨架构构建 `darwin/arm64`、`darwin/amd64` 与 `windows/amd64`。
- **纯 Go 原生 GPU UI 与 MyGo 打包**：桌面端基于 `github.com/egoist/mygo`（macOS Metal / Windows Direct3D 11）与原生系统托盘实现，严禁引入 Electron、WebView、Node.js 或网页前端构建链。MyGo CLI 由 `go.mod` 的 `tool` 指令固定，通过 `go tool mygo` 调用，应用图标（`resources/icon.png`，通过 `resourcesExclude` 排除冗余原图拷贝）、打包与增量更新元数据配置在 `mygo.json`。正式版本号由发版流程从 Git 标签注入 `mygo.json`，仓库内 `mygo.json` 的 `version` 仅用于本地开发构建。
- **菜单栏 / 托盘弹窗与跨平台一致性**：启动默认静默驻留托盘并按需懒创建主窗口（仅传 `--show` 时启动弹窗）；主窗口顶部标题栏左上角使用裁剪自 `resources/icon.png` 的内嵌高清应用 Logo（`internal/ui/assets/logo.png`）；主窗口使用 `TitleBarStyle: mygo.TitleBarHidden` + `HideWindowButtons: true` + `SkipTaskbar: true` + `AlwaysOnTop: true`（在 macOS 隐藏红绿灯、在 Windows 隐藏标题按钮的同时保留双端原生圆角与阴影），点击托盘图标在图标附近弹出/收起，支持 `Esc` 与失焦自动收起（更新下载中、模态弹窗打开及 `--show` 模式不自动收起）；macOS 菜单栏图标**严格使用纯黑透明模板图（`IconIsTemplate: true`，零彩色像素）**由系统随浅/深菜单栏自动切换黑白色，严禁使用彩色原生图标；macOS 与 Windows 均保持常驻 `Menu: nil` 并在 `OnRightClick` 按需弹出快捷菜单（Windows 在右键时按最新快照即时构建并卸载，避免后台定时快照刷新触发 `SetMenuItemInfoW` 导致 Windows 11 亚克力自绘菜单单帧闪烁），确保双端左键弹窗、右键菜单行为完全一致且无闪烁。
- **命令行自动链接 PATH**：`internal/cliinstall` 在正式安装形态运行（非临时目录、非 `/Volumes/` DMG 挂载卷）时自动将 `fsbar` 链接到对应 `PATH` 目录（macOS 优先原地更新已有己方软链，否则按 `/usr/local/bin` → `/opt/homebrew/bin` → `~/.local/bin` 自动建链且不覆盖非己方文件；Windows 通过 `packaging/windows/installer.nsh` 与运行时兜底生成 `fsbar.cmd` 并写入 `HKCU\Environment\Path`，卸载时清理），同时提供 `fsbar install-cli` 与设置页一键重新链接。
- **独立程序主目录与兼容迁移**：默认使用 `~/.flyshadowbar`（Windows 为 `%USERPROFILE%\.flyshadowbar`，支持 `FLYSHADOW_BAR_DIR` 覆盖、兼容 `FLYSHADOW_CONFIG_DIR`）存放 `config.json`、`flyshadow-bar.prefs.json`、`flyshadow-bar.run.json`、`cache/` 与 `logs/flyshadow-bar.log`；严禁向原版 `~/.FlyShadow` 写入任何文件。启动时若未显式指定自定义目录/配置路径，`config.EnsureMigrated()` 会将旧目录 `~/.FlyShadow` 中的 `config.json` 与 `flyshadow-bar.prefs.json` 原子复制至 `~/.flyshadowbar`（不覆盖已有文件、不删除旧文件）；核心库与 GeoIP 在排除显式覆盖（环境变量、可执行文件目录、`~/.flyshadowbar/lib`）后优先从官方 `common-desktop-*.jar` 按条目内容指纹（CRC32 + 大小）解压并刷新至 `~/.flyshadowbar/cache/`，再对 `~/.FlyShadow/lib` 与本地离线缓存保持只读回退兼容；`control.ReadRuntimeFile` 对 `~/.FlyShadow/flyshadow-bar.run.json` 保持过渡期回退读取。
- **自动静默增量更新与代理回退**：`internal/ui/updates.go` 在启动 5 秒后首次检查（若启动初轮失败 30 秒后补检一次）并每 30 分钟周期性静默检查；通过自定义 `mygo.UpdateSource` 优先经由本机内置混合代理访问更新源并自动回退系统代理与直连（未开系统代理同样可更新，且强制 Ed25519 公钥验签），安装就绪后通过弹窗顶部醒目横幅（`renderUpdateReadyBanner`）与托盘菜单提示一键重启生效。
- **切勿覆盖 `mygo.App.Version()`**：`mygo build` 通过 `-ldflags` 注入当前版本号供 `mygo.Updater` 匹配增量补丁；启动时仅在 `mygo.App.Version()` 为空或 `"0.0.0"` 时才回退调用 `mygo.App.SetVersion`。
- **单二进制双形态（CLI 名称 `fsbar`）**：入口位于 `cmd/fsbar/`，无参数或仅带 GUI 标志（`--gui`/`--hidden`/`--config`/`--core-lib`/`--no-connect`/`--no-auto-sysproxy`/`-psn_*`）时启动常驻 GUI；携带子命令时通过本地回环 Bearer Token 控制接口（`~/.flyshadowbar/flyshadow-bar.run.json`，权限 `0600`）与运行中实例通信，且所有子命令均支持 `fsbar help <子命令>` 与 `--help`。
- **包职责划分**：
  - `internal/applog`：自动滚动程序日志（`~/.flyshadowbar/logs/flyshadow-bar.log`，5 MiB × 3 备份、敏感凭据兜底脱敏、panic 堆栈捕获与尾部读取）。
  - `internal/cliinstall`：跨平台命令行工具 `fsbar` 自动链接/注册到系统 `PATH` 目录与冲突保护。
  - `internal/cloud`：官方云端订阅与自定义分流规则双向同步客户端（DoH TXT 发现、代理/直连自动回退、凭据脱敏）。
  - `internal/config`：独立主目录 `~/.flyshadowbar` 管理与旧目录无损迁移、无损配置加载/保存、节点匹配、13 种官方分流规则与自定义规则 CRUD、客户端独立偏好 `flyshadow-bar.prefs.json`。
  - `internal/core`：零 CGO FFI 绑定（macOS `purego`、Windows `golang.org/x/sys/windows`）、核心动态库与 GeoIP 自动定位及 JAR 解压缓存。
  - `internal/sysproxy`：跨平台系统代理（macOS `networksetup`、Windows 注册表 + `wininet.dll`）。
  - `internal/engine`：单进程运行时引擎（隧道热切换、规则热重载不中断隧道、并发测速、1s 流量采样、连接监控、启动自动代理与退出清理）。
  - `internal/control`：仅监听 `127.0.0.1:0` 的随机端口 + 随机 Bearer Token 本地 HTTP 控制服务与客户端。
  - `internal/updater`：GitHub Releases 版本检测、SemVer 比较与按平台挑选产物下载。
  - `internal/ui`：MyGo 原生主窗口、托盘菜单、图标、增量更新管理器与视图（`app.go`、`tray.go`、`updates.go`、`window.go`、`rules_view.go`、`conns_view.go`）。
  - `internal/version`：版本、Commit、BuildDate 与仓库元数据（由 `Makefile` `ldflags` / `MYGO_LDFLAGS` 注入）。
- 常用命令入口统一走 `Makefile`：`make build`、`make run`、`make test`、`make test-live`、`make vet`、`make build-local`、`make build-macos`、`make build-win`、`make release-local`、`make clean`。

## 核心库 FFI 与配置兼容红线

以下行为已通过官方 `common-desktop-*.jar` 与 `libflyshadow_core_lib` 实证核对，修改 `internal/core` 或 `internal/config` 时必须严格遵守：

- `new_proxy(tc)` 传入的是 `tunnel_context`（`tc`），不是 `rt`。
- `get_tunnel_status(rt, tc) == 0` 表示已连接成功，非 `0` 表示未连接或连接中；发起 `connect_tunnel` 前应先调用 `close_tunnel` 清理旧会话避免误判残留状态，就绪轮询窗口与官方一致为 8 秒（`40 × 200ms`），建连超时后必须调用 `close_tunnel` 清理底层会话。
- 核心动态库缓存刷新以 JAR 条目内容指纹（JAR 文件名 + 条目名 + CRC32 + 原始大小，写入 `.meta`）为准，不得仅依赖文件修改时间（DMG 覆盖安装的新 JAR 修改时间可能早于本地旧缓存）；FFI 符号绑定须兼容不同版本官方核心库（如 `2.0.2` 与 `2.0.4`），不得强依赖仅高版本导出的非必需符号。
- `get_tunnel_upload(rt, tc)` 与 `get_tunnel_download(rt, tc)` 在 1 秒采样周期下直接返回当前每秒上下行字节数（Bytes/s），与官方客户端行为一致，严禁对相邻两次读数做差值相减。
- `set_geoip(rt, tc, "CN", dataPtr, len)` 固定使用国家码 `"CN"` 加载 `Country-only-cn-private.mmdb`。
- `start_proxy`（`allowLanConnection=true`）、`start_proxy_ip("127.0.0.1", ...)`（`false`）、`get_version`、`get_tunnel_mapper_info_json` 返回的 C 字符串必须调用 `free_string` 释放；指针转换必须通过 `go vet`（双平台不得出现 `unsafeptr` 警告）。
- `close_connect` 的连接键格式固定为 `source_addr:source_port===protocol`。
- 传输层开关决策保持与官方一致：`enableQuic = udpAccelerationEnabled && (node.enableQuic == 1)`，`enableNativeUdp = nativeUdp && (node.nativeUdp == 1)`。
- `config.json` 必须通过 `map[string]json.RawMessage` 无损原子写回（先写临时文件再 `rename`），严禁丢失官方未建模字段（如账号 token、订阅密码等）；仅本客户端使用的外观/偏好配置一律写入同目录 `flyshadow-bar.prefs.json`。
- 自定义规则完整兼容官方 `customRuleList`、`customRuleActiveLabels`（含 `__DEFAULT__`）、13 种 `matching`（`0~12`）与 3 种 `proxyType`（`0=Direct, 1=Reject, 2=Proxy`），增删改启停后只重载规则，不得断开当前隧道。
- 即时模式（MyGo UI）约束：控件跨帧状态不要保存 `ui.Context` 或 `ui.Element`，异步回调重绘/剪贴板通过 `c.Services()` 触发；不要用 `ui.Tester` 替代真实原生窗口与托盘验收。

## 测试与验收要求

- 代码改动完成后，至少执行以下校验并全部通过：
  ```bash
  CGO_ENABLED=0 go test -v ./...
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go vet ./...
  CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go vet ./...
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
  ```
- 涉及锁、并发、生命周期或热切换逻辑的改动，追加运行对应包的 `-race` 测试。
- 涉及底层 FFI、代理监听或规则热重载的改动，在具备官方核心库的机器上运行 `make test-live`（使用临时高端口，不干扰宿主机代理）。
- 涉及原生 UI 布局、外观主题或托盘交互的改动，除 `internal/ui` 的 `ui.Tester` 单测外，需启动真实构建产物进行原生窗口验收；清理全部 `/tmp/fsbar-*` 等临时验收目录后再结束任务。

## 自动发布流程（维护者与 Agent 专用）

仓库通过 [`.github/workflows/release.yml`](.github/workflows/release.yml) 完成自动化发版，**禁止手工直接创建或修改已公开的 Release**：

1. **发版前检查**：
   - 确认 `main` 分支工作区干净、所有提交已推送至 `origin/main`，且本地单元测试与三平台 `go vet` 全绿。
   - 确认仓库 Actions Secrets 已配置 `DEEPSEEK_API_KEY`（生成更新日志）与 `MYGO_UPDATER_PRIVATE_KEY`（与 `mygo.json` 中 `updates.publicKey` 配对的 Ed25519 私钥，用于签名更新包与生成增量补丁）；任一缺失会直接中断发版。
   - 确认目标版本号遵循 `vX.Y.Z` 语义化规范且尚未公开发布（workflow 会拒绝覆盖已公开版本）。
2. **触发发布**：
   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```
   如需对已有未公开标签重跑，可通过 `workflow_dispatch` 指定 `tag` 触发。
3. **流水线阶段**：
   - `prepare`：校验标签格式与 `origin/main` 祖先关系；收集自上一个已公开标签以来的提交记录（排除 `docs/screenshots`、`README.md` 与 `AGENTS.md`），调用 DeepSeek API（`deepseek-v4-flash`）结合 `.github/release-notes.md` 生成面向用户的简体中文更新日志，创建 Draft Release。
   - `check`：在 `ubuntu-latest` 运行 `CGO_ENABLED=0 go test -v ./...` 及 `darwin/arm64`、`darwin/amd64`、`windows/amd64` 三目标 `go vet`。
   - `build` 矩阵：
     - `macos-15`（注意：`macos-14` 运行器已退役，`macos-latest` 队列易拥堵，固定使用 `macos-15`）执行 `go tool mygo build -platform darwin/arm64,darwin/amd64 -skip-notarize`，生成 Apple Silicon / Intel 双架构 `.dmg`、签名 `.tar.gz`、最近 3 个版本的 `.delta` 增量补丁与 `update-darwin-{arm64,amd64}.json`；
     - `ubuntu-latest` 安装 `nsis` 后执行 `go tool mygo build -platform windows/amd64`，生成 Windows `setup` 安装器、签名 `.tar.gz`、`.delta` 增量补丁与 `update-windows-amd64.json`；
     - 先上传安装包、归档与增量补丁，再上传 `update-*.json` 更新清单。
   - `publish`：下载草稿内全部产物，校验 3 份 `update-*.json`、2 份 macOS `.dmg`、Windows `setup` 安装器以及清单引用的全部归档与 `.delta` 文件大小和签名，输出完整包与增量包体积统计，生成并上传 `checksums.txt`，最后取消草稿并设为 `latest`。
   - `cleanup`：若上述任一阶段失败或被取消，自动删除未公开草稿（保留标签便于修复后重试）。
4. **发布后核验**：
   - 使用 `gh run watch` 或 `gh release view vX.Y.Z` 确认状态为正式公开，且 `darwin-arm64`、`darwin-amd64`、`windows-amd64` 三平台的安装包、签名归档、增量补丁（从第二个签名版本起自动生成）、`update-*.json` 与 `checksums.txt` 齐全。
