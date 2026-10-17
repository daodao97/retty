# 开发、构建与设备测试命令

所有命令从 Retty 仓库根目录执行。日常发布和回归使用 `GOWORK=off`，确保使用 `go.mod` 固定的 MyGo fork；只有调试 MyGo 本身时才使用本地 `go.work`。

## MyGo 依赖

Retty 依赖 MyGo fork `github.com/daodao97/mygo` 的 `main` 分支（包含 iOS 支持及已同步的通用终端能力）：`go.mod` 用 `replace` 固定到该分支上某个已推送提交的伪版本（`v0.0.0-<时间>-<提交>`），不会在构建时自动跟随分支更新。

- 上游更新合并到 fork 的 `main`，保留已合入的 iOS 及通用终端能力；验证并推送后再更新 Retty 的固定依赖。MyGo 改动统一在 `main` 开发。不要让 `go.mod` 指向未推送的提交或本地路径（`replace ... => ../mygo`）。
- 更新依赖：

  ```sh
  GOWORK=off go mod edit -replace github.com/egoist/mygo=github.com/daodao97/mygo@main
  GOWORK=off go mod tidy
  ./scripts/check-mygo.sh
  ```

- `go.work` 指向 `../mygo`，只用于同时修改 MyGo 和 Retty。该目录必须停在 `main`，且与 `go.mod` 固定的提交一致；否则不加 `GOWORK=off` 的命令会悄悄使用另一份 MyGo 代码。`../mygo` 里的未提交或未推送改动不会进入 `GOWORK=off` 构建、GitHub Actions 和正式打包。
- 改完 MyGo 后的顺序：在 `../mygo` 提交并推送 `main` → 更新 Retty 伪版本 → `GOWORK=off` 跑完整检查。
- `./scripts/check-mygo.sh` 只读检查以上约定（固定提交在 fork `main` 上、`go.work` 检出在该分支且与固定提交一致），提交 `go.mod` 改动前、发布前运行。
- 升级 MyGo 后对照 `internal/terminal/UPSTREAM.md`，把 vendored terminal 的本地改动带到新 API 上。上游的 UI API 可能有破坏性变化（例如 #143 的 `ui.Element` 值句柄、`Context` 只在构建期间有效），先运行 `go tool mygo migrate-ui .` 预览并跑全部测试，不能只看编译通过。
- terminal 及其 iOS 构建链（`scripts/build-ios.sh`、libghostty-vt）留在 Retty；其他通用 iOS 原生桥接（扫码、设备信息、收起键盘、网络预热等）放在 MyGo，不要在 Retty 里重新加 cgo/Objective-C。

## 保留真实任务环境

生产构建使用 `go tool mygo build`，不要加 `-debug`。手工 `go build` 默认仍属于 MyGo 开发模式，必须显式加入 `-X github.com/egoist/mygo.production=1`；`mygo_noinspector` 只移除检查器，不能代替生产标志。不要依靠启动时临时设置 `MYGO_ENV` 来发布应用。

2026-10-08 曾把普通 `go build` 产物装进桌面应用，触发当时的旧服务替换逻辑，结束了真实终端进程。当前代码已限制普通应用启动时的自动服务替换；操作时仍按下面的生产流程执行。

- 只正常退出、重开 GUI。会话服务、推送 worker、Codex relay/watch/proxy 和 Agent CLI 可能使用同一个 Retty 可执行文件，不能用 `pkill Retty`、`killall Retty` 或按可执行路径批量结束进程。
- 不对真实环境执行 `go run . -server`，不删除 `server.sock`、应用数据目录、Tailcat identity 或 Keychain 记录来“修复连接”。
- 不用 **Quit and End All Sessions**、`⌥⌘Q` 或客户端 `Restart` 更新正在执行任务的服务。服务协议不兼容或需要新服务功能时，保留旧服务，等任务结束后安排明确的服务更新。
- 测试只连接测试创建的 shell，不向真实 Codex / Claude Code 会话输入测试内容，不要求关闭或重启共享 Codex daemon。
- 更新前后核对服务 PID、会话 ID、shell PID 和终端尺寸。重新打开窗口后等待初始化完成再检查，不能只在 `open` 返回时检查一次。

macOS 会话服务和推送 worker 通过 launchd 启动，再加载用户的登录 shell 配置；不再把 GUI 或自动化命令的整个环境继承给长期运行的服务。Codex 命令工具会注入 `NO_COLOR=1`、pager 等非交互设置，直接从工具环境启动旧版服务会污染之后从桌面、手机新建的会话。正常应用通过 `open /Applications/Retty.app` 启动，不直接执行包内二进制或在真实目录使用 `go run . -server`。用户登录配置中主动设置的环境仍然有效。

launchd 注册只在服务需要启动时创建，不安装开机/登录启动项，不自动重启退出的服务。重复启动请求不会结束正在运行的实例。旧服务已经继承的环境不会因 GUI 更新而改变；有真实任务时保留它，待任务结束后再安排服务更新。

zsh 会话在返回提示符时清空遗留的 Kitty 键盘协议栈，防止 TUI 退出后 Ctrl+C 仍按 CSI-u 编码。ZLE 同时处理已在途的 Ctrl+C 按下/重复事件并忽略松开事件，保留用户已有的按键绑定。注入脚本由会话服务在创建 shell 时写入；GUI 更新不会修改已有 shell 的脚本或升级运行中的服务，这项修复在更新服务后创建的新会话中生效。不要为了启用它重启仍有真实任务的服务。

## 常用检查

可重复的生产版性能测量见 [性能基线](performance.md)。工具为每个场景创建独立会话与 GUI 数据目录，报告 GUI、会话服务、推送 worker 的 CPU、物理内存、RSS、启动峰值及原始样本。

画面同步的新旧协议限速对照见 [画面同步测量](performance-baselines/2026-10-14-screen-sync.md)；它使用独立的内存传输和 Ghostty，不连接真实 daemon，也不模拟无线丢包。

```sh
git status --short
GOWORK=off go test ./...
GOWORK=off go vet ./...
GOWORK=off go vet -tags mygo_noinspector ./...
GOWORK=off go test . ./internal/rex ./internal/remote -race
GOWORK=off go test . -run '^TestName$' -count=1
./scripts/check-mygo.sh
bash -n scripts/build-ios.sh scripts/test-ios.sh scripts/check-mygo.sh scripts/asc.sh scripts/release-ios.sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_ios_release.py'
./scripts/asc.sh workflow validate --output json
```

需要界面测试截图时：

```sh
MYGO_TEST_IMAGES="$PWD/.mygo/test-images" GOWORK=off go test . -run '^TestName$' -count=1
```

## 开发窗口与独立服务

开发窗口使用独立数据目录，不继承真实窗口或终端里的 `RETTY_DIR`：

```sh
env -u MYGO_ENV -u MYGO_READY_SOCKET \
  RETTY_DIR="$PWD/.mygo/dev-data" GOWORK=off go tool mygo dev
```

开发热更新会重新启动开发窗口；服务替换只能用于这种隔离环境。需要本地 MyGo 改动时可去掉 `GOWORK=off`，先运行 `./scripts/check-mygo.sh` 确认 `../mygo` 在 `main` 上（见“MyGo 依赖”）。不要用开发命令更新 `/Applications/Retty.app`。

单独调试服务时另用一个数据目录，也不要与上面的开发窗口并行共用：

```sh
RETTY_DIR="$PWD/.mygo/server-debug" GOWORK=off go run . -server
```

## CLI 服务器构建

CLI 自身更新使用 `retty update --check` / `retty update`；桌面远端会话的命令面板 / Shell 菜单提供「更新远端 Retty…」。请求由远端桥接在独立加密控制流中处理，不转发给会话服务，不通过终端输入执行。CLI 只从固定的官方正式 Release 获取 CLI 和 SHA-256，验证完整运行时后原子切换入口，保留旧文件，关闭并 `exec` 自己的网关进程；macOS 桌面版复用 MyGo 原生更新器的共享安装任务，保持既有 Ed25519 密钥与增量 / 全量下载策略，安装后正常 `App.Relaunch`，仅重开 GUI。两者都不升级会话服务或结束 PTY/Agent。旧端先手动升级一次；桌面开发构建和不可写安装位置仍由原生更新器拒绝。详细行为与部署限制见 [自身更新](cli.md#自身更新和从桌面更新远端)。

```sh
./scripts/check-mygo.sh
GOWORK=off ./scripts/build-cli.sh --platform all
GOWORK=off CGO_ENABLED=0 go test -tags retty_cli \
  ./cmd/retty ./internal/rex ./internal/remote ./internal/terminal/screen
GOWORK=off CGO_ENABLED=0 RETTY_CLI_E2E=1 go test -tags retty_cli \
  ./cmd/retty -run '^TestCLIServerLifecycle$' -count=1 -timeout 3m
GOWORK=off CGO_ENABLED=0 RETTY_CLI_E2E=1 RETTY_CLI_BINARY="$PWD/build/cli/retty-darwin-arm64/retty" \
  go test -tags retty_cli ./cmd/retty -run '^TestGatewayUpdateExec' -count=1 -timeout 2m
```

`cmd/retty` 使用 `retty_cli` 标签，独立于 MyGo UI，不使用桌面生产标志；打包脚本验证依赖里没有图形运行时，并将固定 manifest 的 Ghostty VT 动态库校验后放在二进制旁。`build/cli/` 包含 Linux/macOS × amd64/arm64 的 tar.gz 和 SHA-256 文件。Linux CLI 不依赖 X11、Wayland 或 GUI 会话，当前动态库依赖 glibc，不提供 musl/Alpine 构建。

隔离的 CLI E2E 使用自己的数据目录、服务进程和 shell，验证真实加密隧道、新建会话、输入回显、双端尺寸接管、网关退出保留会话及重连身份。在 Linux 覆盖 `serve` 命令返回后网关继续运行、重复启动复用 PID、`stop` 保留会话及后台 daemon 启动；macOS 使用 `--foreground` 和测试自行启动的会话服务，避免注册测试 launchd job。可用 `RETTY_CLI_BINARY=/absolute/path/retty` 指向已打包的二进制，无需测试内重复构建。

更新的 exec E2E 使用测试专用下载/传输替身，但真实运行管理 socket、进程清理、`exec`、独立会话服务和 PTY，核对网关 PID、会话服务 PID、会话 ID、shell PID 和尺寸不变。下载、SHA-256、归档限制、失败保留旧安装、运行时探测和独立 Tailcat 扩展另有 fixture 测试；此用例不下载或更新真实安装。

`retty serve`（或 `retty`、`retty start`）默认启动后台常驻加密网关并返回，复用已有实例。`retty stop` 只停止网关，不向会话服务发送 `shutdown`；`--foreground` 用于 systemd 等进程管理器，SIGINT/SIGTERM 同样保留会话。管理 socket 为 0600，锁覆盖启动/运行/清理过程，阻止并发实例争用连接身份。默认状态目录是用户配置目录的 `RettyServer`，与已安装桌面应用隔离。用同一份数据目录重启网关会保留 identity 和当前服务；不要为了更新 CLI 结束已有 Agent 任务。安装、运行和 systemd 用户服务示例见 [cli.md](cli.md)。

## 桌面生产构建

### 桌面自动更新

Retty 使用 MyGo 原生 updater 插件。菜单 **Retty → 检查更新…** 和命令面板均可打开更新窗口；默认启动约 10 秒后检查一次，之后每天检查，发现新版本时显示更新说明。下载、验签、安装、跳过版本和重新启动均由 MyGo 处理。应用重启仍走普通退出流程，只保存布局并脱离终端连接，不结束会话服务、推送 worker 或 Agent 任务。后台服务、CLI hook 和 iOS 不初始化 updater。

另一台已配对的 Retty 桌面可从远端会话的「更新远端 Retty…」入口调用同一个原生更新任务。远端检查后确认具体版本，安装任务独立于发起端的控制流；本机窗口正在安装同一版本时共享该任务，本机取消仍有效，不重复下载或并发替换应用。安装成功先保存 `desktop-update.json` 回执，再正常重开远端 GUI。旧版桥接先手动升级一次以获得此入口。

Retty 启用 MyGo 的 `SetKeepPreviousBundles(true)`，将替换的 macOS 包保留在应用旁的隐藏 `.Retty.app.old-<GUI PID>` 路径，供仍运行的后台进程使用资源；不自动清理这些包。任务完成且确认对应进程不再使用后才可清理，不通过批量结束后台进程回收空间。

`mygo.json.updates` 固定 GitHub 来源 `daodao97/retty` 和 Ed25519 公钥。私钥保存在 `~/Documents/Retty-签名与发布备份/MyGo-自动更新/mygo-update.key`，GitHub Actions Secret 为 `MYGO_UPDATER_PRIVATE_KEY`；配套 README 说明恢复方法。不要重新生成或替换密钥，否则已有应用会拒绝新更新。

发布新版本时更新 `mygo.json.version`，并在 `CHANGELOG.md` 中加入对应的 `## <版本>` 更新说明。推送匹配的 `v<版本>` 标签，或运行现有 Release 工作流。Build macOS DMG 构建 universal、arm64、amd64 三个平台，由 MyGo 使用临时 Keychain profile 完成 Developer ID 签名、公证和 stapling，**再**生成签名更新 archive 和增量包；发布阶段验证所有更新签名、URL、版本、大小及完整资产列表。

Release 会同时发布通用 DMG、三个 `update-darwin-*.json`、对应 `.tar.gz` 和可用的 `.delta`。增量包由 MyGo 从已发布的最多三个历史版本生成，失败时客户端回退到完整包。只推送 main 产生 Actions 构建产物，不会向用户发布更新；GitHub Release 发布为 latest 后生效。当前已有的 0.1.1 Release 不含 updater 清单，首次使用需先安装接入 updater 的 0.1.2 或后续版本，以后即可在应用内更新。iOS 继续通过 TestFlight/App Store 更新。

本地仅验证签名更新包时，可读取备份私钥到 `MYGO_UPDATER_PRIVATE_KEY` 后运行生产构建；私钥本身不要写进命令、配置、日志或仓库。构建完成运行 `GOWORK=off go run ./tools/verifyupdates -dir build`，确认三个平台的更新签名和资产。此检查不会发布或安装应用。

### 原生应用图标

选定的深色底、薄荷绿 `>_` 终端图案保存在 `assets/branding/retty-source.png`，生成提示词保存在同目录的 `retty-source.prompt.txt`。修改母图后执行：

```sh
GOWORK=off go run ./tools/mkicon
```

这会导出两份 1024 × 1024 资源：`resources/icon.png` 使用 macOS 原生图标网格的圆角轮廓和透明留白，供 ICNS 回退；`resources/ios/app-icon.png` 为不透明、铺满画布的 iOS 底图，圆角由系统处理。在 macOS 上运行时，还通过 Xcode 26 的 `actool` 将同一底图编译为 Icon Composer 图标堆栈，写入 `resources/darwin/Assets.car`；`macos.infoPlist.CFBundleIconName` 选择其中的 `RettyAppIcon`。macOS 26 使用堆栈的系统蒙版，旧系统使用已裁圆角并留白的兼容图像或 ICNS。生成器检查实际编译出的资源，拒绝缺少堆栈或包含不透明经典图标的 catalog；不要把铺满画布的方形底图放进普通 macOS `.appiconset`，否则 macOS 15 会优先显示它并覆盖 ICNS。不要直接把桌面 ICNS 底图用于手机，否则 MyGo 的透明区域白色填充会产生白边。

`scripts/build-ios.sh` 在自己的临时配置中选择 iOS 图标，仍编译仓库的主包并使用固定 MyGo，结束时删除该临时配置，不修改桌面的 `mygo.json`。覆盖更新手机时，可用 `IOS_PROVISIONING_PROFILE` 指定既有 profile 的名称或 UUID，并用 `IOS_SIGNING_IDENTITY` 指定其已有证书；脚本仅为这次构建启用手动签名，不改变正常配置。手动签名也必须保留 `ios.entitlements` 中的既有 Keychain 访问组，安装前对比新旧应用实际签名的 entitlements。

完整打包包含配置、图标和资源，输出到 `build/darwin-arm64/`：

```sh
env -u MYGO_INSPECTOR GOWORK=off go tool mygo build -platform darwin/arm64 -skip-dmg .
go version -m build/darwin-arm64/Retty.app/Contents/MacOS/Retty
codesign --verify --deep --strict build/darwin-arm64/Retty.app
```

生产构建必须设置 `-X github.com/egoist/mygo.production=1` 和 `mygo_noinspector`，`mygo build` 自动设置两者。Go 1.27 的 `-trimpath` 构建信息不记录 `-ldflags`，因此这类包用 `go version -m` 验证标签，不能据此判断生产标志缺失。需要 DMG 时省略 `-skip-dmg`；Intel Mac 使用 `darwin/amd64`，双架构使用 `darwin/universal`。

GitHub Actions 的 **Build macOS DMG** 工作流在推送 `main` 或手动运行时生成双架构 DMG 及 SHA-256 文件。运行成功后，在该次运行的 Artifacts 中下载 `Retty-macos-universal-dmg`，产物保留 14 天。CI 使用 `GOWORK=off` 和固定版本的 MyGo CLI 进行生产构建，并验证两种架构的 `mygo_noinspector` 标签、应用签名和 DMG 完整性。

### GitHub Release

**Release** 复用 CLI 和 DMG 构建工作流，等待四个平台的 CLI 和 Universal DMG 全部构建、测试和校验通过，先上传到草稿 Release，再公开发布，共五个安装包和五份 SHA-256 文件。正式 Release 强制启用 `require-notarization`：缺少签名 / 公证凭据或 Apple 公证、票据附加、Gatekeeper 校验失败，都会阻止发布，不会回退为 ad hoc 包。发布入口：

- 推送 `v<版本>` 标签，版本必须与 `mygo.json.version` 一致。
- 在 Actions 中手动运行 **Release**，使用所选分支的实际提交和 `mygo.json.version` 创建标签。

同一版本标签不能指向不同提交；新版先更新 `mygo.json.version`。重新运行已公开的同一版本不会覆盖其产物，失败的草稿可重新运行补齐。发布任务才有 `contents: write` 权限；日常构建只读仓库。

日常 `main` 构建没有签名 / 公证凭据时仍可生成 ad hoc 测试包。手动运行 **Build macOS DMG** 可勾选 `require-notarization` 验证正式分发流程；**Release** 始终要求公证。配置位置为仓库 **Settings → Secrets and variables → Actions**：

| Secret | 内容 |
| --- | --- |
| `MACOS_CERTIFICATE_P12` | 包含 **Developer ID Application** 证书和私钥的 `.p12` 文件，base64 编码 |
| `MACOS_CERTIFICATE_PASSWORD` | P12 导出密码；无密码时可留空 |
| `MACOS_NOTARY_KEY` | Apple 团队级 App Store Connect API Key 的 `.p8` 原文，需具备公证权限 |
| `MACOS_NOTARY_KEY_ID` | 该 API Key 的 Key ID |
| `MACOS_NOTARY_ISSUER_ID` | 对应 Issuer ID |

可通过 `base64 < DeveloperID.p12 | gh secret set MACOS_CERTIFICATE_P12` 和 `gh secret set MACOS_NOTARY_KEY < AuthKey.p8` 上传文件，密码使用 `gh secret set MACOS_CERTIFICATE_PASSWORD` 交互输入。不要把证书、私钥或密码放进仓库。

配置后，CI 导入临时 Keychain，用 Developer ID 为应用、内嵌代码和 DMG 签名，启用 hardened runtime 与时间戳，等待 Apple 公证通过并 staple 票据，再验证 Gatekeeper 和生成校验文件。公证失败会阻止产物上传及 Release 发布；部分配置缺失会报错，不退回 ad hoc。任务结束后删除临时凭据。iOS 的 Apple Development / Apple Distribution 证书不能替代 Developer ID Application。

公证提交结果和 Apple 诊断日志保存在 Actions 的 `macOS-notarization-attempt-<尝试序号>` Artifact，每次重跑分别保留 14 天；不包含 P12、P8 或私钥。CI 通过 `scripts/xcrun-ci.py` 为 stapler 的票据附加和验证加入有限重试：仅退出码 68 且明确报告网络超时、DNS / 连接失败或断网时重试，最多 5 次，间隔 2、4、8、16 秒；票据、TLS、签名和其他验证错误直接失败。其他 xcrun 命令直接交给系统工具。构建总超时 45 分钟，单次 Apple 公证等待最多 20 分钟。只有 DMG 和应用的票据、Gatekeeper 检查全部通过后，工作流才输出 `notarized=true`，Release 发布步骤再次检查这一结果。已发布的未公证版本不会原地覆盖，补齐凭据后递增 `mygo.json.version` 再发布。

### 下载后提示已损坏

先区分 DMG 无法挂载，还是复制到 Applications 后应用无法打开。前者用 `hdiutil verify Retty-0.1.0-macos-universal.dmg` 检查镜像；Release 下载同时用 `shasum -a 256 -c Retty-0.1.0-macos-universal.dmg.sha256` 比较校验值，校验失败需重新下载。

当前 ad hoc 包没有 Apple 公证，浏览器下载带有 quarantine 标记，macOS 可能拦截应用并提示“已损坏”。如果确认来源是本仓库构建、包的 SHA-256 和签名完整性验证通过，可将应用拖入 Applications 后，仅移除这个应用的下载隔离标记：

```sh
codesign --verify --deep --strict /Applications/Retty.app
xattr -dr com.apple.quarantine /Applications/Retty.app
open /Applications/Retty.app
```

这只是未公证构建的临时安装方法，不是正式分发修复。正式修复是上述 Developer ID 签名和 Apple 公证，不关闭系统全局 Gatekeeper。

下面是仅修改 Go 代码、资源和应用配置保持一致时的本机更新流程。即使只替换可执行文件，也必须通过 `mygo build` 构建：它会从 `mygo.json` 注入应用版本、更新地址和验签公钥；仅设置生产标志和 `mygo_noinspector` 的手工 `go build` 会禁用自动更新，并显示「无法在当前安装位置自行更新」。先在同一个终端定义只读快照函数；它只发送 `hello`、`list`，不会启动或重启服务，也不保存终端内容、任务提示或 token：

```sh
snapshot_sessions() {
python3 - <<'PY'
import json, os, socket, tempfile
from pathlib import Path
data_dir = Path.home() / 'Library/Application Support/Retty'
path = str(data_dir / 'server.sock')
if len(path.encode()) >= 100:
    value = 2166136261
    for byte in path.encode():
        value = ((value ^ byte) * 16777619) & 0xffffffff
    path = str(Path(tempfile.gettempdir()) / f'retty-{os.getuid()}-{value:x}.sock')
with socket.socket(socket.AF_UNIX) as conn:
    conn.settimeout(3)
    conn.connect(path)
    stream = conn.makefile('rwb')
    def request(number, op):
        stream.write(json.dumps({'id': number, 'op': op}).encode() + b'\n')
        stream.flush()
        response = json.loads(stream.readline())
        if response.get('error'):
            raise RuntimeError(response['error'])
        return response['data']
    hello = request(1, 'hello')
    sessions = request(2, 'list')
    fields = ('id', 'pid', 'cols', 'rows')
    print(json.dumps({'server_pid': hello['pid'], 'sessions': sorted(
        [{key: item[key] for key in fields} for item in sessions],
        key=lambda item: item['id'])}, sort_keys=True, indent=2))
PY
}
```

此函数针对默认的已安装应用目录。若真实应用自定义了数据目录，先修改 `data_dir`，不要在检查失败时启动一个新服务。

在同一个终端执行下列块；任一步失败就停止，保留备份。`mv` 原子替换可执行文件，避免覆盖正在被后台进程使用的文件内容：

```sh
(
set -e
umask 077
MAINT_DIR="$PWD/.mygo/maintenance/$(date +%Y%m%d-%H%M%S)"
APP_DIR=/Applications/Retty.app
mkdir -p "$MAINT_DIR"
snapshot_sessions > "$MAINT_DIR/before.json"
ditto "$APP_DIR" "$MAINT_DIR/Retty-before.app"
env -u MYGO_INSPECTOR GOWORK=off go tool mygo build -platform darwin/arm64 -skip-dmg .
cp build/darwin-arm64/Retty.app/Contents/MacOS/Retty "$MAINT_DIR/Retty"
go version -m "$MAINT_DIR/Retty" | rg -F mygo_noinspector
osascript -e 'tell application id "com.daodao.retty" to quit'
install -m 755 "$MAINT_DIR/Retty" "$APP_DIR/Contents/MacOS/Retty.next"
mv -f "$APP_DIR/Contents/MacOS/Retty.next" "$APP_DIR/Contents/MacOS/Retty"
codesign --force --sign - "$APP_DIR"
codesign --verify --deep --strict "$APP_DIR"
open "$APP_DIR"
sleep 3
snapshot_sessions > "$MAINT_DIR/after.json"
diff -u "$MAINT_DIR/before.json" "$MAINT_DIR/after.json"
echo "会话检查通过；备份目录：$MAINT_DIR"
)
```

本机示例使用 ad hoc 签名。正式分发沿用 Developer ID、entitlements 和公证流程，不用它替代发布签名。更新过程中不要另行创建、关闭或调整会话，否则快照差异需要人工核对。GUI 能打开不代表检查通过；服务 PID、原会话及其尺寸必须保持一致，推送 worker 和现有 Agent CLI 也应继续运行。可用 `ps -p PID1,PID2 -o pid=,comm=` 检查更新前记录的具体 PID。

如果资源或 `mygo.json` 有变化，应安装完整生产包，先正常退出 GUI，将新包放入临时目录，再替换应用包；仍须执行同样的前后快照检查。旧服务和已有 worker 继续运行旧代码，更新 GUI 不代表它们已升级。若检查失败，停止后续操作并保留日志，不要再通过服务重启补救；回滚 GUI 也不能恢复已经结束的 PTY。

## iOS 构建、签名与安装

移动端在前台查看会话详情时通过 MyGo `Power.KeepAwake` 保持屏幕常亮；返回列表、离开应用或进入后台时立即释放，回到详情页后重新申请。重复渲染不会累积请求，也不改变系统的手动锁屏行为。

应用启动画面由 `mobile_launch.go` 用 MyGo 的 `ui.Image`、`ui.Text` 和布局组件绘制，居中显示 Retty Logo 和应用名，颜色随系统主题切换。MyGo 的 `ui.Startup` 管理首帧确认；Retty 通过 `Ready` 告知初始本地数据已加载，并使用 `SkipTransition` 直接进入首页，不设置固定展示时长或额外渐变等待。Go 启动前的系统占位交给 MyGo 默认机制，应用不通过 `ios.launchScreen` 配置自己的布局。

Retty 的 Bundle ID 为 `com.daodao.retty`，启动页和应用名称均为 Retty。首次安装该身份时，签名 profile 和 APNs App ID 必须覆盖这个 Bundle ID；使用既有 Team。这个新身份使用自己的容器、Keychain 命名空间和 `Retty` 数据目录，不迁移先前应用的数据。已有任务与旧应用保持运行，设备测试仍只使用独立 fixture。

构建会准备对应的 Ghostty 静态库，再调用固定版本的 MyGo CLI。设备和模拟器的静态库不同，不要并行运行两种构建：

```sh
IOS_TEAM=YOUR_EXISTING_TEAM_ID
IOS_DEVICE=YOUR_IPHONE_UDID
GOWORK=off ./scripts/build-ios.sh -ios-team "$IOS_TEAM" -ios-device "$IOS_DEVICE"
codesign --verify --deep --strict build/ios-arm64/Retty.app
xcrun devicectl device install app --device "$IOS_DEVICE" build/ios-arm64/Retty.app
xcrun devicectl device process launch --device "$IOS_DEVICE" --terminate-existing com.daodao.retty
```

`-ios-device` 和 `IOS_DEVICE` 使用 `xcrun xctrace list devices` 显示的硬件 UDID；`devicectl` 也接受它自己的设备 ID。用 `xcrun devicectl list devices` 查看设备。新机器先检查 Xcode SDK 与已有签名身份：

```sh
xcode-select -p
xcrun --sdk iphoneos --show-sdk-version
security find-identity -v -p codesigning
xcrun xctrace list devices
xcrun devicectl list devices
```

安装 Retty 后，后续更新继续使用该手机已有的 Team、`com.daodao.retty` Bundle ID 和 provisioning profile，覆盖安装即可。不要为了安装失败卸载正常应用或切换 Team，避免丢失数据或改变 Keychain 访问。profile 必须覆盖该设备；需要推送时还须包含匹配的 `aps-environment`。`mygo.json` 的 `ios.developmentTeam` 是默认值，不能据此推断另一台手机也使用这个 Team。mini 的常规终端/连接测试与具备 APNs entitlement 的设备测试分开核验。

```sh
GOWORK=off ./scripts/build-ios.sh -ios-simulator
```

上面生成的是模拟器包，不能装到真机。构建后如有跟踪文件变化，用 `git diff` 检查，不要将临时生成的静态库、签名文件或私密 fixture 提交。

### App Store Connect / TestFlight

统一使用 `./scripts/release-ios.sh` 和仓库中的 `.asc/workflow.json`。MyGo 负责 Ghostty/Go/UIKit 生产构建、archive、App Store Connect 导出和本地校验；[App Store Connect CLI](https://github.com/rorkai/App-Store-Connect-CLI) 负责工作流编排、远端 build number、上传、处理状态和 TestFlight。MyGo 不调用 ASC，也不持有上传凭据。

`scripts/asc.sh` 自动下载 `.asc/toolchain.json` 固定的 ASC 5.14.0 并核对 SHA-256，缓存到 `.mygo/tools/asc/asc`，不依赖全局安装版本。最终目录固定如下，不再按任务、日期或版本新建打包目录：

| 产物 | 固定位置 |
| --- | --- |
| IPA | `build/app-store/ios-arm64/Export/Retty.ipa` |
| Archive / dSYM | `build/app-store/ios-arm64/Retty.xcarchive/` |
| MyGo 校验报告 | `build/app-store/ios-arm64/BuildReport.json`、`ExportReport.json` |
| 版本、SHA-256、工具版本、源码提交 | `build/app-store/Release.json` |
| 构建日志 / 上传回执 | `build/app-store/Build.log`、`Upload.json` |

MyGo 在内部临时 staging 目录完成校验后原子替换固定的 `ios-arm64/`；失败保留上次完整产物。不要去掉这种内部隔离，也不要把临时目录当成最终交付路径。工作流用文件锁避免同时覆盖发布包；上传和恢复步骤核对 IPA SHA-256，拒绝使用被另一轮打包替换的产物。已记录的成功上传回执可复用，后续等待/分组不会重新上传。

本地签名先在 Xcode → Settings → Accounts 登录既有 Team；出现 `missing Xcode-Token` / `No Accounts` 时重新登录，不删除证书或正常应用。工作流强制 `GOWORK=off`、既有 Team 和 `app-store-connect` 导出，取消真机开发 profile 的环境覆盖，不安装或启动真实应用。

ASC API 认证独立于 Xcode 登录。只在首次配置时用已有 App Store Connect API key 登录，私钥放在仓库外；默认使用系统 Keychain，`.asc/` 的认证和运行状态不提交：

```sh
./scripts/asc.sh auth login --name Retty \
  --key-id YOUR_KEY_ID --issuer-id YOUR_ISSUER_ID \
  --private-key /private/path/AuthKey_YOUR_KEY_ID.p8 --network
./scripts/asc.sh auth status --validate
./scripts/asc.sh workflow validate --output json

# 默认通过 ASC 查询此版本的下一可用 build number，然后只打包
./scripts/release-ios.sh package

# 离线打包时显式指定尚未上传的 build number
./scripts/release-ios.sh package BUILD_NUMBER:3

# 核对当前包；上传既有包并等待这个确切版本/build；查询这个包的处理状态
./scripts/release-ios.sh check
./scripts/release-ios.sh upload
./scripts/release-ios.sh status

# 一次完成打包、上传、等待、加入指定 TestFlight 组
./scripts/release-ios.sh testflight TESTFLIGHT_GROUP:Internal

# 预览或恢复同一轮流程；恢复时不再传新的 KEY:VALUE
./scripts/release-ios.sh testflight TESTFLIGHT_GROUP:Internal --dry-run
./scripts/release-ios.sh upload --resume RUN_ID
```

API key 为 individual 类型时，登录使用 `--key-type individual` 并省略 issuer。应用默认按 `mygo.json` 的 Bundle ID 精确查找；可用 `ASC_APP_ID` 指定已有 App Store Connect app ID，脚本仍核对 Bundle ID，防止传错应用。营销版本统一来自 `mygo.json.version`，工作流不修改这个文件。build number 查询不是远端原子预留，不要在其他机器同时发布同一个版本。

`ExportReport.json` 必须通过分发签名、App Store profile、生产推送权限及符号检查；工作流额外确认 profile 包含实际分发签名证书、禁用权限没有链接进 IPA、包与发布清单一致。`BuildReport.json` 检查的是 archive 中的开发签名，不能代替分发检查。TestFlight 分组需要显式指定，流程不会默认通知测试者或提交 beta/App Store 审核。

`ios.capabilities` 启用 `camera`（扫码）、`microphone` 和 `speech`（按住说话转文字），并提供对应用途说明。iOS 和 macOS 的 `infoPlist` 均提供 `NSLocalNetworkUsageDescription`，说明同一 Wi-Fi 下的加密直连用途；系统未允许本地网络访问时，局域网直连可能不可用。MyGo 不编译闲置的位置、照片授权和 Face ID 接口，普通 Keychain 存储不引用生物识别接口。通知权限仍由用户主动开启。若新增受保护功能，需同步启用对应 capability 并填写实际用途说明。

Retty 的 Tailcat 隧道包含 Apple 系统之外的加密实现。目前省略两个出口加密键，上传后在 App Store Connect 如实完成加密问卷。不要仅为了通过构建声明豁免。确认属于豁免时才设置 `ITSAppUsesNonExemptEncryption=false`；需要文稿时，等 Apple 批准后设置 `true` 和匹配的 `ITSEncryptionExportComplianceCode`。MyGo 会拒绝 `true` 配空/缺失代码、错误类型或自相矛盾的键，避免再次出现 `Invalid Export Compliance Code []`；本地检查无法证明代码与远端文稿匹配。参见 [Apple 加密声明说明](https://developer.apple.com/documentation/bundleresources/information-property-list/itsappusesnonexemptencryption) 与 [确认并上传加密文稿](https://developer.apple.com/help/app-store-connect/manage-app-information/determine-and-upload-app-encryption-documentation)。

## 隔离的远程连接与真机测试

远程手机及桌面查看器请求可协商的 `screen_sync` 模式，版本号和最低协议版本保持 6/4。旧 daemon 忽略新 attach 字段，继续返回原始 ANSI 或带尺寸的字节帧；新 daemon 的 `Hello.screen_sync` 和 attach 回复广告支持。只更新 GUI 不会升级正在运行的 daemon，旧端继续原模式；等真实任务结束，在正常服务维护中更新 daemon，禁止为了激活这项能力重启正在运行的服务。

新模式首帧只序列化可见网格。画面帧携带尺寸、递增序号与服务端版本，后续基于已确认快照发送完整状态或字节差分；客户端先重建完整 VT 再原子替换模拟器，下次读取才确认上一帧已消费。每个查看器最多一帧未确认、一个 dirty 唤醒标记，发送间隔依据交付和确认耗时调整，最低 16ms。快照上限 4MiB；写入或确认超过 15 秒只关闭该 attachment。输入仍有序写入 PTY、不做离线重放或预测回显。桌面独立的输入/尺寸 attachment 同时请求 `input_only`，首帧之后不再重复传终端输出。

新服务端统一使用 headless Ghostty VT 保存状态；没有传统字节查看器时由服务端直接回复终端查询，传统查看器在场时保留原来的回复方式。响铃、OSC 52 写剪贴板和桌面通知通过独立的有界一次性事件队列传递，不能像过时画面一样丢弃；事件积压超限只断开慢查看器。程序的 DEC 2026 绘制事务提交后才发送画面，停止输出的未结束事务使用 250ms 的观察超时；原子状态帧不把绘制锁带到客户端。每 2 秒有画面心跳，静默会话正常确认；超过 5 秒没有完整画面确认时显示更新延迟，旧端不会因没有输出而显示该提示。

新模式的滚动历史通过独立只读快照查看：桌面命令面板 / View 菜单选择「查看终端历史…」，手机终端页右上角历史按钮。初始请求最多 512 行历史，再按需扩大到 2048 / 8192 行；回复最多 8MiB，15 秒超时。历史请求独占一个可取消的控制连接，不阻塞主控制轮询或画面确认；历史弹层不附着 PTY、不输入、不调整尺寸、不把当前任务结果标为已读。Live 画面的后续更新不改变正在阅读的历史快照，关闭释放自己的请求和模拟器；历史弹层中的关闭窗格 / Tab 快捷键只关闭弹层。

Tailcat 隧道首次 TCP 拨号成功后，即在后台使用 `DiscoPing` 尝试直连；手机首页的预连接也启动探测，点击连接后复用同一个探测 worker 和已有路径指标。失败或中继时先以 2、4、8、16 秒间隔重试，然后每 30 秒检查一次，直连成功后直接进入 30 秒间隔；单次 3 秒超时，不阻塞连接初始化或创建终端 attachment。所有活动及预连接隧道共用一个按需启动的网络监听器，接口/地址变化或系统唤醒时立即唤醒探测并重置短间隔重试；已建立隧道上的新拨号也唤醒探测，不重建隧道或会话。最近连接的指标弹层打开时最多每 10 秒额外采样一次，与后台探测串行共享同一隧道。探测随隧道关闭取消，不依赖短暂的拨号 context，也不会触发会话重连；最后一个探测 worker 退出时关闭网络监听器。请求耗时、中位数/P95、超时、重连和画面恢复统计来自已有轮询与恢复流程，仅在内存保留最近 5 分钟、最多各 256 条记录。正常切 tab 后保活到期的接入不计为异常画面恢复；请求耗时包含远端处理时间，超时次数不代表网络丢包。手机连接诊断保持原有流程。

Tailcat 服务端会永久保留每个连接过的节点密钥，并在每次新连接时向所有历史对端发送端点通告。因此每台设备在数据目录（桌面 `remote/client-secret`）或 Keychain（手机 `tunnel-secret`）保存一个私有种子，按桌面派生 4 个固定密钥槽：新隧道选用一个既未在用、又已关闭超过 10 秒的槽（刚关闭的密钥在桌面端仍信任旧的直连 UDP 路径，立即复用会让握手等待 WireGuard 5 秒重试），槽都不可用时才退回临时密钥。种子载入前、CLI 和测试仍使用临时密钥。手机首页的在线探测成功后，隧道会保留 45 秒，下一次探测或点击连接直接在其上建立控制连接；离线桌面的探测间隔从 30 秒逐步退避到 5 分钟，进入后台时关闭保留的隧道。手机同样在后台做直连探测。从后台恢复时，保留的控制连接 2 秒内无响应就在同一隧道上并行重拨，取先成功者。`list` 响应带 ETag，内容未变时只回 ETag 不回列表；这是新增字段，旧版两端会忽略，不需要提升协议版本。

桌面远程窗格使用独立的只读画面流和前台输入/尺寸 attachment，兼容现有 v4/v6 服务，无需重启 daemon。离开窗格或窗口失焦立即关闭前台 attachment 并释放尺寸；只读画面流保留 60 秒，后台继续消费输出，超时关闭但保留终端画面。返回时复用画面流，通过新的 LIST 检查所有权后接入输入；已被其他设备接管的会话保持只读，必须点击“接入当前窗格”。后台流不发送输入、resize 或租约续期。前台期间两条流都会接收输出，输入流会持续排空，避免远端写入阻塞。首次载入或实际画面流恢复超过 400ms 才显示载入提示；单纯恢复输入不会清屏或显示载入提示。

桌面文件拖拽通过独立的 `upload-files` 桥接连接上传，不向会话服务发送文件数据或新操作，也不要求升级已有 daemon。桌面桥接与 CLI 网关均使用自己的状态目录下的 `uploads/batch-*/` 保存完整批次；文件权限为 0600，可执行文件和目录为 0700。各个拖入项有独立子目录，同名文件不会覆盖。只有收到完整内容、客户端完成标记并确认目标会话仍存活后，才返回绝对路径；失败批次自动删除，完成批次保留以供运行中的程序使用，不随窗格关闭删除。

单批最多 1GB、4096 项、256 个拖入项，超时 10 分钟。文件夹中的符号链接或特殊文件不展开，需单独拖入链接目标；顶层符号链接会解析到目标。取消、切换窗格、失去窗口焦点或断开连接会停止传输，并丢弃迟到的路径结果。旧版远端提示升级桥接/网关，不回退粘贴本机路径。只更新远端 GUI 即可让桌面桥接支持上传；已有 CLI 网关需待合适时机更新，不重启正在运行任务的会话服务。

这些 Go 测试自行创建临时服务、Tailcat 桥和 shell，不连接日常任务：

```sh
RETTY_REMOTE_E2E=1 GOWORK=off go test ./internal/remote \
  -run '^TestTailcatSessionLifecycle$' -count=1 -v -timeout 25m
RETTY_MOBILE_RECOVERY_E2E=1 GOWORK=off go test . \
  -run '^TestMobileRecoveryAcrossDesktopBridgeRestart$' -count=1 -v -timeout 3m
```

真机脚本安装上一步已签名的 `build/ios-arm64/Retty.app`，使用独立 Keychain 命名空间和测试服务：

```sh
GOWORK=off IOS_TEAM="$IOS_TEAM" IOS_DEVICE="$IOS_DEVICE" ./scripts/test-ios.sh
GOWORK=off IOS_TEST_FLOW=background IOS_TEAM="$IOS_TEAM" IOS_DEVICE="$IOS_DEVICE" ./scripts/test-ios.sh
GOWORK=off IOS_TEST_FLOW=input IOS_TEAM="$IOS_TEAM" IOS_DEVICE="$IOS_DEVICE" ./scripts/test-ios.sh
```

`full` 流程覆盖扫描、会话、键盘、选择和旋转；`background` 流程验证后台 45 秒复用同一控制连接，以及控制连接断开后回到同一会话、桌面尺寸不变；`input` 流程在独立 shell 中验证连续英文、两次中文九宫格候选提交及后续英文的实际输入顺序，并复查上下滑动收起键盘。手机需有英文和中文拼音九宫格键盘。XCTest 的测试专用启动参数是 `RETTY_UI_TEST=1`，普通使用不要保留它。

测试后恢复正常启动：

```sh
xcrun devicectl device process launch --device "$IOS_DEVICE" --terminate-existing com.daodao.retty
```

日志和 xcresult 在 `.mygo/ios-test/`；临时 `tests/ios/Fixture.swift` 含连接能力，应由脚本清理且不提交。只清理自己创建的测试服务、runner 和临时目录。不要批量结束所有 Retty、Go 或 Python 进程。

## 桌面会话布局

设置 → 外观 → 会话布局可选择「Tab 模式」或「侧边栏模式」，默认保持经典顶部 Tab。侧边栏模式将同一组 Tab 显示在左侧，右侧终端填满剩余区域；点击边栏图标或按 `⌃⌘S` 收起/展开，收起后终端占满窗口宽度。`⌘1` 在 Tab 模式与展开的侧边栏模式之间切换；两种模式都不用 `⌘` 加数字选择 Tab。菜单栏不设 Tabs 菜单，切换与重命名 Tab 位于 Shell 菜单。布局及折叠状态保存在 `settings.json`，切换不会重建会话、改变分屏树或清除缩放；终端尺寸随可用空间调整。

两种模式共用 Tab 的选择、拖动排序、重命名、关闭和右键操作，也保留已有快捷键、远端电脑标识与 Agent 状态。侧边栏列表只在切换选中 Tab 或重新展开时滚动到选中项，普通滚轮浏览不会被拉回。

侧边栏的标签页列表最多占窗口高度的 40%，下方「项目会话」独立滚动，按工作目录列出本机 Codex / Claude Code 历史，项目按其中最近活跃会话的时间倒序排列。项目名称和箭头均可展开、折叠；点击已打开的会话会激活原标签页和窗格，未打开时才在新本机标签页精确 resume，并沿用原配置目录。历史扫描在后台执行，MyGo `Outline` 只渲染可见行；刷新和恢复的隔离用例见 `sidebar_history_test.go` 与 `internal/agenthistory/history_test.go`，不读取开发者真实历史。详细行为见 [Agent 会话恢复](agent-recovery.md)。

终端分屏使用 MyGo `SplitPercent` / `SplitVerticalPercent`，在现有 `Node.Ratio` 与百分数之间转换，保留保存的布局格式。框架负责分隔条拖动、键盘方向键调整、无障碍范围和窄窗口约束；窗口缩小不覆盖已选比例，恢复空间后继续使用原比例。Retty 只补充双击均分和共享分隔线的焦点边框。图标操作使用 `ButtonBase`，统一支持 Tab 聚焦、Enter / Space 激活和禁用状态。

## 桌面文件浏览器

右上角文件夹按钮、View → 文件浏览器、命令面板或 `⇧⌘E` 切换文件工作台；经典 Tab 和侧边栏模式共用这一内容视图。点击后用左侧文件树和右侧文件内容占满内容区，终端暂时隐藏、保留原有尺寸与会话；返回终端恢复显示，不重建会话。默认关闭，开关、浏览 / Diff 模式、树宽度和隐藏文件选项保存在 `settings.json`；拖动中间分隔线调整树宽度，双击恢复 260px。

文件树与内容区使用 MyGo `Split`，分隔线只占 1pt，具有更宽的鼠标命中区域，也可聚焦后用方向键调整；宽度停止变化 250ms 后合并保存，关闭文件工作台时立即保存。浏览 / Diff 使用 `SegmentedBase` 保留现有样式，由框架管理单选状态、方向键切换与焦点组。

文件工作台跟随当前窗格及其工作目录；手动浏览其他目录后可从菜单回到会话目录。文件树默认宽 260px；单层顶部栏提供「浏览 / Diff」切换，目录与文件的低频操作分别收进「…」菜单。「打开目录…」用弹层输入路径，代码内容使用留白、圆角卡片和文件标题行，增删统计显示在标题中。「浏览」模式按需加载展开的目录，不递归扫描整棵树；「Diff」模式用只读 `git status --porcelain=v1 -z` 列出全仓库变更，构建可折叠的层级树，支持已删除文件。文件树始终保留，点击文件在右侧显示内容或差异。树和文本 / Git 变更使用 MyGo 虚拟列表；预览可放大，支持行内选择复制和复制整个已加载内容。`⌘F` 聚焦文件筛选框；隐藏的终端不计为结果已查看。目录缓存最多 256 个目录 / 20000 项，刷新清理缓存。PNG/JPEG 图片预览限制为 1600 万像素，文件内容从 256KB 起按需扩大至 4MB；目录扫描最多 5000 项并明确显示截断提示。另存为分段读取，每次最多 1MB、单文件最多 1GB，检查文件版本，取消或变化时清理请求自己的临时文件，不替换已有目标。

本机读取与远端读取共用 `internal/files` 的只读服务；远端 `files-browse` 请求由已认证 Tailcat 连接上的 Bridge / CLI 网关直接处理，不送入 PTY daemon，也不发送终端输入或取得尺寸控制。并发最多 4 个，请求可取消；旧桥接返回明确的升级提示，离线远端不会退回本地目录。连接码沿用已有整机访问权限，文件操作使用桥接进程的操作系统用户权限。仅新增桥接能力，无需提升会话协议或重启真实 daemon。

Git 预览使用只读命令显示工作区（含已暂存修改）相对 HEAD 的单文件变更；未跟踪文件和首次提交前的文件按新增文件显示。禁用外部 diff、textconv、fsmonitor、pager 和可选索引锁，不暂存、提交或执行项目脚本。代码预览使用 MyGo 官方 [`ui/editor.View`](https://mygo.egoist.dev/docs/ui/code-editor)，以只读模式提供语法高亮、行号、折叠、查找、横向滚动及跨行选择复制。Diff 使用同包的 [`DiffView`](https://mygo.egoist.dev/docs/ui/diff-viewer) 和 `SetPatch` 显示服务返回的单文件补丁，保留原文件行号和行内变更高亮；不启用编辑、接受或回退操作。组件状态随已加载的预览保留，放大预览复用它；主题与窗口明暗模式同步。未知语言保留纯文本，不再限制展示行数或截断超长行；读取范围仍为初始 256KB、按需加载到最多 4MB。预览菜单复制已加载的原始字节内容，保留制表符和 CRLF；图片、二进制提示及下载沿用既有流程。

## 弹层和命令面板

等待授权或回答时在对应 Tab / 窗格显示状态；完成和失败通过通知提醒，点击通知直达会话，不再提供独立的「待处理」入口或列表。当前结果窗格在前台且可见时记录已读；列表浏览或通知送达不算已读，查看不会解除等待。

已读和通知投递回执分开保存，事件身份沿用 `AgentNoticeID`。新会话服务通过 `Hello.attention` 广告已读能力，`markAttention` 仅接受当前事件的 ID，已读结果随 `list` 在桌面/手机之间同步；源服务原子保存到 `attention-seen.json`，不触碰 PTY、尺寸或输入。旧服务降级到客户端的 256 条已读记录，继续沿用桌面 `inbox-seen.json`、手机 SecureStore `inbox-seen-v1` 的存储键，保留既有回执；旧服务不能跨端同步。

Codex 只从当前根线程、当前轮的 `item/completed` 最终回复生成摘录，仍等明确的完成/空闲事件确认结束；Claude Code 使用 Stop/StopFailure 的 `last_assistant_message`。Codex TUI 会通过同一连接创建 `ephemeral` 临时线程生成旧对话摘要等结构化内容；proxy 和 metadata watcher 不允许临时线程或子 Agent 接管窗格、生成完成通知，即使它们的 `turn/start` / `turn/steer` 请求成功。摘录为首个正文句，最多 160 字符，不包括代码块，不调用模型，不保留完整回复。新一轮会清除旧摘录；没有最终回复时沿用任务说明。桌面、远端提醒与 APNs 共用 `AgentNoticeBody`。需要源服务、对应 hook/proxy 和推送 worker 支持新字段；GUI 更新不会升级已有后台进程，不为了启用摘要或已读同步重启真实任务。Agent 恢复记录同时保存事件序号，重启电脑后继续递增，避免旧已读/通知回执吞掉恢复后的下一轮结果。

本地控制轮询仍为 500ms，维护和重连检查照常执行；相同快照不再请求重建界面。输出、状态、摘要、已读、尺寸变化及未完成的窗格维护仍请求刷新，终端输出和输入继续自行失效。隔离用例 `TestIdlePollFramesAndLifecycleMaintenance` 的 20 次相同快照从原先 20 次刷新请求降到初始化的 1 次。Apple M3 上 `BenchmarkIdleSnapshotComparison`（200ms）的 1/32/100 个会话约为 110ns/2.7µs/8.3µs，均零分配；这是快照比较成本，不能换算为整机 CPU 降幅。复现：`GOWORK=off go test . -run '^$' -bench BenchmarkIdleSnapshotComparison -benchtime=200ms`。

命令面板通过 MyGo `ComboboxBase.Inline` 在原布局内显示选项；MyGo 负责高亮、上下键、点击/回车选择、无障碍和高亮变化时的滚动。Retty 保留模糊筛选、稳定的命令/窗格 ID 和执行动作。Esc 或点外部关闭后恢复终端焦点，命令主动打开搜索框时保留该搜索框的焦点。

命令面板输入 `ssh` 列出本机 `~/.ssh/config` 中的具体 `Host` 别名，输入 `ssh <关键词>` 按别名模糊筛选，回车或点击在新的本机标签页运行 `ssh -- <别名>`。即使当前标签页连接另一台 Retty 电脑，也使用本机配置和会话服务。主机列表每次打开面板异步重新读取；支持一行多个别名、引号、注释、去重，以及 `Include` 的绝对路径、相对 `~/.ssh` 路径、`~/` 和 glob；忽略通配和否定的 Host 模式，不执行 `Match exec`。这里只发现别名，连接时的用户名、端口、密钥、跳板和条件配置由 OpenSSH 自己解析（[配置格式](https://man.openbsd.org/ssh_config)）。

手机设备重命名、会话编辑及结束确认使用 MyGo `DialogBase.Dismissed()`，统一处理 Esc、点外部和按钮关闭后的键盘收尾。取消不保存草稿；设备重命名取消后返回会话列表设置。连接指标使用 `PopoverBase.Dismissed()` 清理当前弹层标记。

## 推送配置与状态

电脑设置 → Agent → 通知提供「桌面通知」和「手机通知」两个独立开关，默认均开启。「桌面通知」控制本机全部系统提醒，关闭时也撤掉当前应用持有的通知；其下完成/失败、等待输入两个细分选项保留原值，重新开启总开关后继续使用。关闭桌面通知不会自动改为手机通知，仍沿用原有在场/离开路由。「手机通知」控制此电脑发出的手机任务提醒，独立于手机会话列表设置里的本机「手机通知」开关；两边都允许才会推送。

手机发送开关由新版 push worker 在观察事件、领取路由和实际发送前读取原子保存的 `settings.json`，退出 GUI、租约过期或 worker 重启后仍生效。关闭会丢弃已排队/重试的手机提醒并取消尚未完成的发送，重新开启只提醒未来事件，不清除设备配对和 APNs key；已经由 APNs 接受的请求不能撤回。旧 worker 不认识此设置，只有升级 GUI 不能保证停止它的后台推送；继续保留旧 worker，待安全的维护时机更新，不能为了开关重启正在运行任务的服务。

桌面与 APNs 共用 push worker 的事件领取记录。前台当前窗格静默；系统未锁屏、未休眠且键鼠闲置不足 5 分钟时，只发桌面通知；离开电脑后优先发到最近联系过发送端的一台手机，没有可用 APNs provider 或手机订阅时才保留桌面提醒。APNs 不提供实时手机在线查询，不能以手机 Retty 的前台连接作为后台推送的必要条件。

窗口每秒上报系统状态、查看的 Host / Session 和通知偏好，租约 5 秒过期。远程桌面通过既有加密控制连接上报 presence，通过独立的 `notification-claim` 桥接请求向会话源电脑领取通知，避免多个桌面各自发送。这个请求不附着 PTY，不改终端尺寸；无需更新会话协议。完整统一路由需要源电脑的 GUI 桥接与 push worker 支持 `routingVersion=1`。旧 worker 保留运行，GUI 通过旧 foreground 字段传递“人在电脑前”的粗略租约；旧版桥接无法实现完整跨端领取。不要为了启用通知路由重启有真实任务的服务，待合适的维护时机更新 worker 和桥接。

同一事件已查看、已领取桌面通知或 APNs 已接受时持久记录，切换焦点、回到桌面或重启 worker 不重放它。APNs 请求已在发送时，不再让桌面领取同一事件；平台接受推送不等于手机已显示通知。桌面通知仍依赖 macOS 的 Retty 通知许可。离开判定是系统状态与闲置时间的估计，不是物理在场检测。

```sh
GOWORK=off go tool mygo push status
/Applications/Retty.app/Contents/MacOS/Retty -push-status
```

首次配置才导入已有的 APNs key：

```sh
GOWORK=off go tool mygo push setup /private/path/AuthKey_KEYID.p8
```

私钥、设备 token、Tailcat link、profile 和临时 fixture 不放进仓库或完整日志。不要为了更新 GUI 重新生成连接 identity、导入 key 或重启推送 worker。APNs 详细接入见 README 的 iPhone 部分及 MyGo 的推送文档。

## 手机终端链接

手机会话中的网页链接可直接轻点，在默认浏览器打开；终端的 OSC 8 显示文字和跨行 URL 都按当前显示位置命中。轻点在松开时才打开，滑动滚屏、取消触摸和长按选择不打开链接。通用 `OpenLinksOnTap` 能力先合入 MyGo fork 的终端插件，再同步到 Retty 的 vendor；桌面保留 Command / Control 点击。

## 手机语音输入

会话详情底部直接显示「按住说话」胶囊按钮，左侧键盘按钮切换键盘。终端输入或草稿编辑唤起键盘时隐藏整行语音按钮，收起键盘后恢复；草稿编辑器与「发送」按钮保持可用。进入页面仅查询权限；首次按住才请求麦克风与语音识别权限，授权后再按住开始录音。按住时在输入栏上方显示随应用主题变化的轻量面板，实时展示识别文字，不改变终端尺寸。松开结束识别，上滑 64pt 后松开取消，滑回按钮可撤销取消；无需额外选择「转文字」。结果先进入可编辑草稿，点击「发送」将文字写入当前会话并提交一次回车；草稿中的换行和控制字符转为空格，避免变成多次提交。

MyGo 的 `Speech` API 提供 Apple 设备端识别、实时完整文本、停止/取消、60 秒录音上限及识别结束超时；Retty 只绘制 UI 并绑定当前会话。离开页面、切换会话或进入后台会取消，旧回调不会写入新会话。`ios.capabilities` 声明 `microphone` 和 `speech`，并配置两个用途说明。设备/语言不支持离线识别时显示原因，不自动改用云端。后续豆包可实现 MyGo `SpeechProvider`，复用手势和草稿确认流程。
