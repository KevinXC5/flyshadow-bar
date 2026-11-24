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
- 涉及对外展示或提交仓库的截图，必须使用完全虚构的脱敏测试配置（模拟节点、规则与连接数据），严禁截取用户真实 `~/.FlyShadow/config.json` 界面。
- 本地功能验证、代理实测或运行 `FLYSHADOW_LIVE_TEST=1` 时，必须使用临时目录配置与随机/隔离高端口，并设置 `--no-auto-sysproxy`（或 `DisableAutoSysProxy: true`），严禁修改用户真实的 `~/.FlyShadow/config.json`、系统代理设置或占用宿主机正在运行的代理端口（如 `7890`）。

## 架构与构建约束

- **全程零 CGO（`CGO_ENABLED=0`）**：所有构建、测试与交叉编译必须保持 `CGO_ENABLED=0`，不得引入 C 源码、CGO 依赖或需要 C 编译器的库；确保在 macOS / Linux 上可以直接交叉编译 `GOOS=windows GOARCH=amd64`。
- **纯 Go 原生 GPU UI**：桌面端基于 `github.com/egoist/mygo`（macOS Metal / Windows Direct3D 11）与原生系统托盘实现，严禁引入 Electron、WebView、Node.js 或网页前端构建链。
- **单二进制双形态**：入口位于 `cmd/flyshadow-bar/`，无参数或仅带 GUI 标志（`--gui`/`--hidden`/`--config`/`--core-lib`/`--no-connect`/`--no-auto-sysproxy`/`-psn_*`）时启动常驻 GUI；携带子命令时通过本地回环 Bearer Token 控制接口（`~/.FlyShadow/flyshadow-bar.run.json`，权限 `0600`）与运行中实例通信。
- **包职责划分**：
  - `internal/config`：无损配置加载/保存、节点匹配、13 种官方分流规则与自定义规则 CRUD、客户端独立偏好 `flyshadow-bar.prefs.json`。
  - `internal/core`：零 CGO FFI 绑定（macOS `purego`、Windows `golang.org/x/sys/windows`）、核心动态库与 GeoIP 自动定位及 JAR 解压缓存。
  - `internal/sysproxy`：跨平台系统代理（macOS `networksetup`、Windows 注册表 + `wininet.dll`）。
  - `internal/engine`：单进程运行时引擎（隧道热切换、规则热重载不中断隧道、并发测速、1s 流量采样、连接监控、启动自动代理与退出清理）。
  - `internal/control`：仅监听 `127.0.0.1:0` 的随机端口 + 随机 Bearer Token 本地 HTTP 控制服务与客户端。
  - `internal/updater`：GitHub Releases 版本检测、SemVer 比较与按平台挑选产物下载。
  - `internal/ui`：MyGo 原生主窗口、托盘菜单、图标与视图（`app.go`、`tray.go`、`window.go`、`rules_view.go`、`conns_view.go`）。
  - `internal/version`：版本、Commit、BuildDate 与仓库元数据（由 `Makefile` `ldflags` 注入）。
- 常用命令入口统一走 `Makefile`：`make build`、`make run`、`make test`、`make test-live`、`make app`、`make win`、`make release-local`、`make clean`。

## 核心库 FFI 与配置兼容红线

以下行为已通过官方 `common-desktop-*.jar` 与 `libflyshadow_core_lib` 实证核对，修改 `internal/core` 或 `internal/config` 时必须严格遵守：

- `new_proxy(tc)` 传入的是 `tunnel_context`（`tc`），不是 `rt`。
- `get_tunnel_status(rt, tc) == 0` 表示已连接成功，非 `0` 表示未连接或连接中。
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
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
  ```
- 涉及锁、并发、生命周期或热切换逻辑的改动，追加运行对应包的 `-race` 测试。
- 涉及底层 FFI、代理监听或规则热重载的改动，在具备官方核心库的机器上运行 `make test-live`（使用临时高端口，不干扰宿主机代理）。
- 涉及原生 UI 布局、外观主题或托盘交互的改动，除 `internal/ui` 的 `ui.Tester` 单测外，需启动真实构建产物进行原生窗口验收；清理全部 `/tmp/fsbar-*` 等临时验收目录后再结束任务。

## 自动发布流程（维护者与 Agent 专用）

仓库通过 [`.github/workflows/release.yml`](.github/workflows/release.yml) 完成自动化发版，**禁止手工直接创建或修改已公开的 Release**：

1. **发版前检查**：
   - 确认 `main` 分支工作区干净、所有提交已推送至 `origin/main`，且本地单元测试与双平台 `go vet` 全绿。
   - 确认仓库 Actions Secret 已配置 `DEEPSEEK_API_KEY`（缺失或调用失败会直接中断发版，不生成残缺公告）。
   - 确认目标版本号遵循 `vX.Y.Z` 语义化规范且尚未公开发布（workflow 会拒绝覆盖已公开版本）。
2. **触发发布**：
   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```
   如需对已有未公开标签重跑，可通过 `workflow_dispatch` 指定 `tag` 触发。
3. **流水线阶段**：
   - `prepare`：校验标签格式与 `origin/main` 祖先关系；收集自上一个已公开标签以来的提交记录（排除 `docs/screenshots` 与 `README.md` 纯展示变动），调用 DeepSeek API（`deepseek-v4-flash`）结合 `.github/release-notes.md` 生成面向用户的简体中文更新日志，创建 Draft Release。
   - `check`：在 `ubuntu-latest` 运行 `CGO_ENABLED=0 go test -v ./...` 及 `darwin/arm64`、`windows/amd64` 双平台 `go vet`。
   - `build-macos`：在 `macos-latest`（注意：`macos-14` 运行器已退役，勿回退）执行 `make app` 构建 `flyshadow-bar-<tag>-darwin-arm64.zip` 并上传至草稿。
   - `build-windows`：在 `ubuntu-latest` 执行 `make win` 零 CGO 交叉编译，上传 `flyshadow-bar-<tag>-windows-amd64.zip` 与 `flyshadow-bar-<tag>-windows-amd64.exe` 至草稿。
   - `publish`：下载草稿内全部 3 个产物做完整性校验，生成并上传 `checksums.txt`，最后取消草稿并设为 `latest`。
   - `cleanup`：若上述任一阶段失败或被取消，自动删除未公开草稿（保留标签便于修复后重试）。
4. **发布后核验**：
   - 使用 `gh run watch` 或 `gh release view vX.Y.Z` 确认状态为正式公开且 4 个资产（macOS zip、Windows zip、Windows exe、`checksums.txt`）齐全。
