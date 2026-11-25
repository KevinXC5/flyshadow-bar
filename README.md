# FlyShadow Bar

基于 [`github.com/egoist/mygo`](https://github.com/egoist/mygo)（纯 Go、零 CGO、Native GPU UI）打造的轻量跨平台 FlyShadow 替代客户端与内置 CLI，直接调用官方底层 Rust 原生核心库，彻底告别官方 JVM / Compose 客户端的高内存占用与沉重体积。

## 界面预览

<table>
  <tr>
    <td align="center" width="50%">
      <img src="docs/screenshots/nodes-dark.png" alt="深色模式 · 节点列表与实时速率图" width="360" />
      <br />
      <sub>深色模式 · 实时速率折线图、订阅流量卡与节点列表</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/screenshots/nodes-light.png" alt="浅色模式 · 节点列表" width="360" />
      <br />
      <sub>浅色模式 · 跟随系统外观自适应</sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/screenshots/subscribe-modal-dark.png" alt="订阅密码配置弹窗" width="360" />
      <br />
      <sub>内置订阅密码配置 · 校验并同步节点、流量与云端规则</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/screenshots/rules-dark.png" alt="自定义规则管理" width="360" />
      <br />
      <sub>自定义规则管理 · 分组启停、双向云端同步与热生效</sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/screenshots/conns-dark.png" alt="实时连接监控" width="360" />
      <br />
      <sub>实时连接监控 · 真实客户端进程识别与一键加直连/代理</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/screenshots/settings-dark.png" alt="系统设置" width="360" />
      <br />
      <sub>系统设置 · 代理端口、网络分流、外观主题与自动更新</sub>
    </td>
  </tr>
</table>

## 资源占用对比（vs 原版 FlyShadow）

原版桌面客户端基于 JVM + Compose Desktop + Skiko，仅后台常驻就会消耗数百 MB 物理内存。**FlyShadow Bar** 采用纯 Go 单进程架构与 `mygo` 原生 GPU 渲染（macOS Metal / Windows Direct3D 11），无 JVM、无 Electron、无 WebView 子进程，在 macOS（Apple Silicon）同机加载同一份订阅配置（9,300+ 分流规则与 CN GeoIP）实测对比如下：

| 对比维度 | 原版 FlyShadow（JVM / Compose） | FlyShadow Bar（纯 Go / MyGo） | 优化幅度 |
| --- | --- | --- | --- |
| **常驻物理内存（Footprint）** | ~619 MB | **~58 MB** | **直降 ~90%（节省 560+ MB）** |
| **应用安装体积（macOS `.app`）** | ~234 MB | **~14 MB** | **缩小 ~94%** |
| **分发压缩包体积** | ~90+ MB | **5.0 MB（macOS）/ 5.6 MB（Windows）** | **极致轻量** |
| **运行时依赖** | 内置整套 JRE + Skiko | **零运行时依赖（单二进制）** | **冷启动即开即用** |
| **连接监控后台开销** | 常驻刷新 | **仅打开「连接」页按需轮询** | **常驻空闲 0% CPU** |

> 注：内存数据取自 macOS 实机 `top` 的进程物理内存（`MEM`），实际数值会随系统版本与连接数略有波动。

## 核心特性

- **单进程直连原生核心库**：macOS 通过 `purego`、Windows 通过 `x/sys/windows` 以零 CGO 方式加载官方底层动态库（`libflyshadow_core_lib.dylib` / `flyshadow_core_lib.dll`），自动加载 GeoIP 与分流规则。
- **菜单栏与系统托盘原生弹窗**：启动默认静默驻留 macOS 菜单栏（隐藏 Dock 图标，可选等宽字体实时速率显示）与 Windows 系统托盘（隐藏任务栏占位），不提前分配窗口对象；**左键点击图标直接在图标附近弹出无红绿灯/无边框按钮的原生圆角面板**（支持按 `Esc` 或点击面板外部自动收起），**右键弹出快捷菜单**（模式切换、节点子菜单、系统代理、UDP 加速、原生 UDP、一键测速与安全退出），macOS 与 Windows 交互一致。
- **紧凑高容量原生 UI**：浅色 / 深色 / 跟随系统外观自适应；概览区并排内置实时上下行速率折线图与订阅流量卡（流量进度、到期时间、VIP 徽标、一键同步与密码配置）；弹窗按屏幕高度自适应（大屏单屏可见 11+ 节点），支持搜索、收藏、按延迟排序与一键测速。
- **独立订阅配置与双向云端同步**：支持零配置启动并在订阅卡片弹窗直接填写/更换订阅密码（先云端校验成功再安全落盘，失败不覆盖旧凭据），日常更新节点、流量、到期时间与管理云端自定义规则均无需再打开官方客户端；规则增删改与启停双向同步云端并立即热生效，**无需断开隧道**。
- **实时连接监控与精准进程识别**：对接核心库 `get_tunnel_mapper_info_json` 与 `close_connect`，macOS 下自动批量反查连向本地代理的真实客户端进程（避免误标为代理自身），查看目标域名、进程名、路由结果与实时流量，并可一键加为直连/代理规则、踢掉旧连接即刻生效。
- **单二进制内置 CLI（自动链接 PATH）**：无参数运行即启动常驻 GUI；安装运行后自动将命令行工具 `fsbar` 链接/注册到系统 `PATH` 目录（macOS 自动软链至 `/usr/local/bin`、`/opt/homebrew/bin` 或 `~/.local/bin`，Windows 安装器与运行时自动生成 `fsbar.cmd` 并写入用户 `PATH`），支持终端与 SSH 远程运维，全部命令支持 `--json` 输出。
- **跨平台系统代理与安全退出**：支持 macOS / Windows 系统代理一键开关、启动连上后自动开启（同步 `autoSetSysProxyOnStartUp`），退出时自动关闭系统代理并安全断开隧道。
- **无损配置写回与独立数据目录**：默认使用独立程序主目录 `~/.flyshadowbar`（Windows 为 `%USERPROFILE%\.flyshadowbar`），不与原版 `~/.FlyShadow` 混写；首次运行或升级时若存在旧配置则自动无损复制迁移，按 `map[string]json.RawMessage` 原子写回 `config.json`，完整保留账号 token、订阅密码等所有原生字段；界面偏好与滚动程序日志独立保存在同一目录下。
- **自动静默增量更新**：基于 MyGo 原生更新器与 Ed25519 签名校验，优先经由本机内置混合代理访问更新源并自动回退系统代理与直连（未开系统代理同样可用）；启动后及每 30 分钟自动检测新版本，后台静默下载增量补丁（`.delta`）并验签安装，就绪后在弹窗顶部一键「重启更新」；同时保留 CLI `fsbar update` 手动检查与下载能力。

## 自动查找规则

启动时会自动初始化程序主目录并定位配置文件、原生核心库与 GeoIP 数据库，所有路径均支持环境变量覆盖。

### 程序主目录与配置文件

FlyShadow Bar 使用独立主目录保存配置、界面偏好、运行态文件、缓存与程序日志：

- **macOS**：`~/.flyshadowbar`
- **Windows**：`%USERPROFILE%\.flyshadowbar`

首次安装或从旧版本在线更新时，若目标目录尚无 `config.json` / `flyshadow-bar.prefs.json` 且本机存在 `~/.FlyShadow/` 旧文件，应用会自动将其无损复制至 `~/.flyshadowbar/`（绝不修改或删除原版目录）。

配置文件按以下优先级查找：

1. 环境变量 `FLYSHADOW_CONFIG_PATH` 指定的完整路径。
2. 环境变量 `FLYSHADOW_BAR_DIR`（兼容 `FLYSHADOW_CONFIG_DIR`）下的 `config.json`。
3. 默认主目录：`~/.flyshadowbar/config.json`（Windows 为 `%USERPROFILE%\.flyshadowbar\config.json`）。

同一主目录下还包含：
- `flyshadow-bar.prefs.json`：客户端自身的轻量界面偏好。
- `flyshadow-bar.run.json`：运行时本地控制发现文件（权限 `0600`，退出自动清理）。
- `logs/flyshadow-bar.log`：自动滚动程序日志（单文件上限 5 MiB，保留 3 份历史归档，可在「设置 → 关于与更新」一键打开目录或运行 `fsbar logs` 查看）。
- `cache/`：从官方 JAR 解压的核心动态库与 GeoIP 缓存。

### 原生核心库

按以下优先级查找（显式设置 `FLYSHADOW_CORE_LIB` 环境变量时优先使用）：

- **macOS**（`libflyshadow_core_lib.dylib`）：
  1. 可执行文件同级目录、`.app/Contents/Resources`、`.app/Contents/Frameworks` 及当前工作目录。
  2. `~/.flyshadowbar/lib/<最新版本>/` 显式放入的核心库（按语义化版本号降序选取）。
  3. 自动扫描 `/Applications/FlyShadow.app/Contents/app/common-desktop-*.jar`（及 `~/Applications/` 下对应路径），按当前架构提取 `darwin-aarch64/libflyshadow_core_lib.dylib`（Apple Silicon）或 `darwin-x86-64/libflyshadow_core_lib.dylib`（Intel）并按条目内容指纹（CRC32 与大小）缓存刷新至 `~/.flyshadowbar/cache/`，官方客户端升级后自动同步最新核心库。
  4. 只读兼容 `~/.FlyShadow/lib/<最新版本>/libflyshadow_core_lib.dylib`（按语义化版本号降序选取）。
  5. 回退使用 `~/.flyshadowbar/cache/` 中已缓存的离线副本（卸载官方客户端后仍可继续运行）。
- **Windows**（`flyshadow_core_lib.dll`）：
  1. 可执行文件同级目录及当前工作目录。
  2. `%USERPROFILE%\.flyshadowbar\lib\<最新版本>\` 显式放入的核心库（按语义化版本号降序选取）。
  3. 自动扫描常见官方安装目录（`%LOCALAPPDATA%`、`%ProgramFiles%`、`%ProgramFiles(x86)%` 下的 `FlyShadow` 与 `Programs\FlyShadow`）内的 `common-desktop-*.jar`，按条目内容指纹提取并缓存刷新 `win32-x86-64/flyshadow_core_lib.dll` 至 `%USERPROFILE%\.flyshadowbar\cache\`；其次查找上述安装目录及其 `app`、`lib`、`resources` 子目录中直接释放的 `.dll`。
  4. 只读兼容 `%USERPROFILE%\.FlyShadow\lib\<最新版本>\flyshadow_core_lib.dll`。
  5. 回退使用 `%USERPROFILE%\.flyshadowbar\cache\` 中已缓存的离线副本。

### GeoIP 数据库

按以下优先级查找 `Country-only-cn-private.mmdb`：

1. 环境变量 `FLYSHADOW_GEOIP` 指定的路径。
2. 可执行文件候选目录与 `~/.flyshadowbar`（含 `geoip` 子目录）中显式放置的文件。
3. 自动从官方 `common-desktop-*.jar` 中的 `geoip/Country-only-cn-private.mmdb` 按条目内容指纹提取并缓存刷新至 `~/.flyshadowbar/cache/`。
4. 只读兼容 `~/.FlyShadow`（含 `geoip` 子目录），并回退使用 `~/.flyshadowbar/cache/` 离线副本。

### 环境变量一览

| 环境变量 | 说明 |
| --- | --- |
| `FLYSHADOW_BAR_DIR` | 自定义 FlyShadow Bar 程序主目录（默认 `~/.flyshadowbar`，兼容旧变量 `FLYSHADOW_CONFIG_DIR`） |
| `FLYSHADOW_CONFIG_PATH` | 自定义 `config.json` 完整路径 |
| `FLYSHADOW_BAR_LOG_FILE` | 自定义程序日志文件路径（默认 `~/.flyshadowbar/logs/flyshadow-bar.log`） |
| `FLYSHADOW_CORE_LIB` | 显式指定原生动态库路径（`.dylib` / `.dll`） |
| `FLYSHADOW_GEOIP` | 显式指定 `Country-only-cn-private.mmdb` 路径 |
| `FLYSHADOW_BAR_RUN_FILE` | 自定义运行态发现文件路径（默认 `~/.flyshadowbar/flyshadow-bar.run.json`） |
| `FLYSHADOW_UPDATE_URL` | 可选镜像/测试更新源目录（仍强制官方 Ed25519 公钥验签） |

## CLI 使用说明（`fsbar`）

命令行工具名称为 **`fsbar`**。正式安装包安装并运行后会自动完成 `PATH` 链接（macOS 自动软链至可写的 `/usr/local/bin`、`/opt/homebrew/bin` 或 `~/.local/bin`，Windows 安装器与运行时自动配置 `fsbar.cmd` 并加入用户 `PATH`）；若手动移动应用位置或通过 `make build` 源码构建，也可执行 `fsbar install-cli` 或在「设置」页一键重新链接。

无参数运行 `fsbar` 将默认静默启动至菜单栏 / 系统托盘（点击图标弹出面板，附加 `--show` 可启动时立即弹窗）。应用运行期间，可在终端直接调用以下子命令进行控制（均可附加 `--json` 输出结构化数据，运行 `fsbar help <子命令>` 或 `fsbar <子命令> --help` 查看完整参数与示例）：

| 子命令 | 别名 | 说明 |
| --- | --- | --- |
| `status` | - | 查看当前连接状态、节点、代理模式、端口、传输协议与实时速率 |
| `ls` | `nodes` | 列出所有可用订阅节点、收藏状态、QUIC/UDP 标签与延迟 |
| `node <query>` | `use <query>` | 按节点 ID、精确名称或关键词热切换当前节点 |
| `mode [mode]` | - | 查询或切换代理模式，支持 `rule`（规则）、`global`（全局）、`direct`（直连） |
| `udp [on\|off\|toggle]` | - | 开启、关闭或切换 UDP 加速（QUIC）并热重连 |
| `native-udp [on\|off\|toggle]` | - | 开启、关闭或切换原生 UDP 转发并热重连 |
| `sysproxy [on\|off\|toggle]` | - | 开启、关闭或切换操作系统代理 |
| `ping` | - | 触发全量节点并发延迟测速并按延迟排序输出 |
| `reload` | - | 重新加载 `config.json` 与域名分流规则并刷新引擎状态 |
| `sync` | - | 从云端同步最新订阅节点、套餐流量、到期时间与自定义规则 |
| `rules` | `rule ls` | 列出所有自定义分流规则（ID、状态、匹配类型、规则值、动作、标签） |
| `rule add <value>` | - | 快速添加自定义规则并立即热生效（支持 `--match`、`--action`、`--label`） |
| `rule toggle <id>` | - | 启用或禁用指定自定义规则并热生效 |
| `rule rm <id>` | `rule del` | 删除指定自定义规则并热生效 |
| `conns` | `connections` | 查看底层核心库当前的实时活跃连接列表 |
| `logs [-n N] [--path]` | `log` | 查看程序最近滚动日志或输出日志文件路径（无需桌面端运行） |
| `version` | `-v` / `--version` | 输出客户端版本号、提交哈希与构建时间 |
| `update` | - | 检查 GitHub Releases 最新版本（`--download` 自动下载安装包） |
| `install-cli` | - | 将当前 `fsbar` 自动链接/注册到系统 `PATH` 目录（支持 `--dir` 指定目录） |
| `help [cmd]` | `-h` / `--help` | 查看完整帮助或单个子命令的详尽参数说明 |

### 常用示例

```bash
# 查看总览帮助或单个子命令详尽说明
fsbar --help
fsbar help rule
fsbar mode --help

# 查看运行状态与全部节点
fsbar status
fsbar ls

# 按关键词或节点 ID 热切换节点
fsbar use 香港
fsbar node 12

# 切换代理模式与 UDP / 系统代理开关
fsbar mode rule
fsbar udp on
fsbar native-udp toggle
fsbar sysproxy on

# 全量测速与热重载配置
fsbar ping
fsbar reload

# 自定义规则快捷管理（立即热生效，不中断隧道）
fsbar rules
fsbar rule add openai.com --match suffix --action proxy --label ai
fsbar rule add 192.168.0.0/16 --match cidr4 --action direct
fsbar rule toggle 1
fsbar rule rm 1

# 查看实时活跃连接
fsbar conns

# 查看版本与检查更新
fsbar version
fsbar update --download
```

## 本地开发与测试

项目全程使用 `CGO_ENABLED=0` 构建，无需安装 C 编译器。

```bash
# 直接运行
make run

# 编译本机可执行文件到 bin/fsbar
make build

# 运行全量单元测试
make test

# 运行核心库实机冒烟测试（使用临时高端口，不干扰宿主机代理）
make test-live

# 清理编译与打包产物
make clean
```

## 打包与交叉编译

项目通过 `go tool mygo build`（由 `go.mod` 固定版本）完成多平台生产构建与签名增量更新打包，自动注入生产标志并剥离符号表。

```bash
# 快速构建当前系统架构应用（跳过 DMG、安装器与公证）
make build-local

# 构建 macOS 双架构（Apple Silicon arm64 + Intel amd64）应用包与 DMG
make build-macos

# 零 CGO 交叉编译 Windows amd64（需本机或 CI 安装 NSIS 以生成安装器）
make build-win

# 本地一键构建全平台（darwin/arm64、darwin/amd64、windows/amd64）
make release-local
```
