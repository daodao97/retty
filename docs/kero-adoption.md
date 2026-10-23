# Kero 借鉴功能实施与验收

状态：八项实现与隔离验收完成。范围来自 2026-10-23 对话中的完整计划；工作区现位于 `quickgui/retty`。已覆盖安装本机桌面应用和 iPhone，运行中的会话服务、推送 worker 和 Agent 进程保持原实例。

## 交付清单

- [x] Git 分支摘要：窗格展示分支、增删行数和合并状态；点击进入对应 Diff；后台读取，有界缓存，不阻塞 UI。
- [x] Worktree 归组：同主机、同 Git common directory 的会话归于同一项目；保留各自工作目录、分支和恢复身份。
- [x] 只读轮次 Diff：Agent 开始时记录独立基线；比较本轮、HEAD 和分支 merge-base；不改工作目录或用户 index，包含未跟踪文件；共同修改只表示期间差异。
- [x] Agent CLI：稳定目标身份，支持 list/status/read/new/split/send/wait 与 JSON；先读取后控制；等待命令返回真实退出结果；附 Agent 使用 skill。
- [x] 统一 Action：菜单、命令面板、快捷键与 CLI 共用稳定 ID；明确可调用动作，保留关闭/重启等既有边界。
- [x] 显式状态报告：脚本可报告 working/blocked/done/error、进度与子任务；复用现有 Agent 事件与通知，不替换原生事件判断。
- [x] 图片预览：终端图片路径悬停预览，远端从对应主机读取；请求可取消、迟到结果丢弃，限制字节数及像素数；评估并接入 Kitty 终端图片。
- [x] 浏览器窗格：与终端共同保存布局和 URL，支持本地开发页、浏览、点击、输入、等待、截图；远程开发端口可访问；维护 WebView 生命周期。

## 验收约束

- 现有未提交改动保持原样；MyGo 固定到已推送 fork main，验证使用 `GOWORK=off`。
- 不结束真实会话、PTY、推送 worker 或共享 Codex daemon，不为启用新代码重启真实服务。
- Git 快照使用独立 index，不暂存用户文件；只读功能不执行 Git 外部 diff/textconv 或项目脚本。
- 自动化和 UI/服务测试使用专用临时数据、仓库和会话，不向真实 Agent 输入测试内容。
- 运行对应单元/集成测试、完整 Go 测试、vet（含生产标签）与相关 race 检查；GUI 用隔离目录验证。
- 每项以实际代码、测试和运行证据验收，未实现或未验证的条目保持未勾选。

## 当前证据

### 第一阶段：Git 状态与 Worktree

- `internal/gitstate` 读取 common directory、分支、HEAD、增删行数、上游差距和 Git 操作状态；关闭外部 diff、textconv、fsmonitor 与可选索引锁，清除继承的 Git 目录覆盖。
- `desktop_git.go` 在后台通过现有独立文件通道查询；最多 4 个并发请求、256 个缓存项；按主机、连接和目录隔离，连接变化不复用旧结果。设置 → 外观可关闭状态查询。
- `sidebar_history.go` 在历史工作线程解析项目身份，最多缓存 512 个目录；linked worktree 与主检出共享项目，恢复时仍使用原始目录和会话身份。
- 按用户后续要求，活动 Tab 恢复原来的会话顺序，不显示额外项目文件夹标题；项目历史仍保留 Worktree 归组。定向用例验证不显示标题，以及点击后仍选择原 Tab/PTY。
- 定向用例覆盖脏 index 不变、unborn/detached、独立 Git 元数据、linked worktree、名字中的换行、过大文件、取消、迟到响应、主机/连接隔离和点击 Diff。
- 完整测试：`GOWORK=off go test ./...` 通过（`.mygo/kero-stage1-tests.log`）。
- 静态检查：`GOWORK=off go vet ./...` 及 `go vet -tags mygo_noinspector ./...` 通过（`.mygo/kero-stage1-{vet,production-vet}.log`）。
- 定向 race：桌面、Git、文件服务、远端桥接的上述用例通过（`.mygo/kero-stage1-race.log`）。远端测试移除自有 daemon socket 后仍可读取 Git 摘要，证明未依赖 PTY 服务。

### 阶段顺序

实现顺序为独立快照与只读 Last Turn Diff → Agent CLI 与 Action 注册 → 图片 → 浏览器窗格与自动化。各阶段的代码与验收证据分别列在下文。

### 第二阶段：轮次 Diff

- 私有 Git 对象库保存 raw 快照和不可变比较，原仓库 index/objects/refs 不变；覆盖 HEAD、merge-base、本轮三种基线。
- 服务接受规范化轮次事件后记录；Codex 要求根线程的 canonical turn ID。每次请求核对服务公布的轮次身份；缺失、失败和旧服务不会复用上一轮。
- 本机与远端通过现有独立文件请求读取，远端隔离测试在移除自有 daemon socket 后仍能列出并打开本轮 Diff。
- 终端旁提供范围选择、文件列表、只读预览、刷新、窄窗口布局及打开完整文件工作台。图像验收：`.mygo/kero-adoption/turn-diff-beside-terminal.png`。
- 定向跨层用例通过：`.mygo/kero-turn-complete-tests.log`。完整测试通过：`.mygo/kero-stage2-tests.log`；普通及生产标签 vet 通过：`.mygo/kero-stage2-{vet,production-vet}.log`；跨层定向 race 通过：`.mygo/kero-stage2-race.log`。
- 捕获在释放服务的公共 Agent 锁后进行，按会话串行，下一轮或关闭会取消旧捕获；原生 Hook 的记录预算为一秒，Codex 独立观察器最多两秒。锁范围与 Agent 路由调整后的 race 回归通过：`.mygo/kero-turn-lock-recheck.log`。
- 产品与存储说明见 [git-review.md](git-review.md)。

### 第三阶段：CLI、Action 与状态报告

- `internal/controlcli` 为桌面和无 UI CLI 提供相同命令；ID 为完整服务会话身份或唯一前缀，保留原有网关 `status`。读取和控制均不自动启动、重启或替换服务。
- 历史读取使用独立连接，不附着或接管尺寸；`send` 写入一次，可校验读取时的输出版本，连接错误不自动重试。新任务保留 argv，`wait --exit` 返回真实进程结果。
- `internal/desktopcontrol` 提供权限 0600、单拥有者、有界的 GUI 端点；主线程队列校验窗口与取消，调用者截止时间传入队列。分屏在后台创建会话后放置，复用轮询已发现的窗格，避免重复附着。关闭、重启、清屏和更新等 Action 仅保留 GUI 入口。
- `commands.go` 注册稳定 ID；菜单、快捷键、命令面板与 CLI 使用同一批对象。命令面板的内部身份不再依赖可翻译标题。
- `report` 复用 OSC 7501 的字段验证、层级记录、通知版本及去重，不破坏分段终端解析；原生 Agent 事件继续生效。`retty skill` 可在无服务时读取内嵌的 `retty-control` skill（已通过 skill-creator 校验）。
- 真实 CLI 进程测试验证退出 23、超时 124、退出后读屏及原有网关状态：`.mygo/kero-control-process-e2e.log`。GUI/CLI 联调覆盖目标分屏，图像：`.mygo/kero-adoption/cli-created-splits.png`（已查看）。
- 完整测试通过：`.mygo/kero-stage3-tests.log`；无图形 CLI 包测试通过：`.mygo/kero-stage3-cli-tests.log`；普通及生产标签 vet 通过：`.mygo/kero-stage3-{vet,production-vet}.log`；控制、状态及相关回归 race 通过：`.mygo/kero-stage3-race.log`；截止时间及队列取消后续 race 检查通过：`.mygo/kero-control-cancellation.log`。
- 完整回归中修正了两处测试前提：Worktree 项目目录使用规范路径但恢复身份保留原路径；尺寸锁在首帧布局提交后记录原桌面尺寸。两项重复回归通过：`.mygo/kero-stage3-rechecks.log`。
- 使用说明见 [session-control.md](session-control.md)。未更新真实安装或运行中的服务，新服务能力通过 Hello 显式探测，旧服务仍保留任务。

### 阶段衔接

浏览器窗格、布局与 URL 恢复、浏览/点击/输入/等待/截图自动化及远程开发端口的证据见第五阶段；此前未完成时保留未勾选，补齐后统一验收。

CLI 复用 `internal/rex.Client` 与 `internal/terminal/screen`；`RETTY_SESSION`、`RETTY_AGENT_SOCKET`、`RETTY_DIR` 由新服务创建会话时注入。浏览器自动化扩展独立 GUI 控制端点，不使用 PTY 输入作为传输通道。

本段中用户询问 Hook 的 ENOENT 错误：已确认仓库由 `gorex` 改名到 `retty`，当前 Codex 会话仍使用旧 cwd。已在仓库外建立兼容链接 `quickgui/gorex -> retty`；旧 cwd 的 shell 启动、已配置的 RTK Hook 和默认工具执行均验证退出 0。未修改全局 hooks 或重启进程。

### 第四阶段：终端图片

- `internal/imagepreview` 统一文件版本读取与解码前限制；默认最多 4 MiB、16 Mi 像素、单边 8192。终端悬停约 350ms 后开始读取，最多两个解码工作线程；捕获主机/连接/目录身份，离开、隐藏或关闭时取消并释放图片，迟到结果不能替换新预览。
- 本机和 Retty 远端复用独立文件服务。远端主机路由测试用本机红图和远端蓝图验证不会回退到本机同名路径；移除自有 daemon socket 后，桥接仍可解码远端图片。普通 ssh/mosh 不猜测文件主机。
- 同步 MyGo 的 Kitty 绑定、PNG 解码与三层绘制，保留 Retty 的 iOS/CLI/重排/屏幕通道。默认每终端图片存储和单张 PNG 像素各限 8 MiB，拒绝文件和共享内存媒介。支持直接 RGB/RGBA/PNG、分块、压缩、裁剪、偏移、层级、替换和删除；虚拟 Unicode placeholder 与上游绘制实现一样尚不绘制。
- 增加直接像素及位置的可恢复快照，像素传输按 generation 缓存。`screen_graphics` 附着协商后使用最大 16 MiB 的图片状态帧；旧查看端保持原有文本状态限制，普通历史读取不携带图片。后台屏幕首次查看前即有单元格像素几何，进程退出后保留可见图片产物。
- 定向测试通过：`.mygo/kero-images-complete-targeted.log`；覆盖 PNG 格式/上限、迟到/取消、主机路由、图片替换/删除/裁剪/层级/光标、无 UI 屏幕与真实隔离服务普通/同步连接的重连和退出后恢复，核对源会话 PID/尺寸不变。
- 完整测试：`.mygo/kero-stage4-tests.log`；CLI 测试：`.mygo/kero-stage4-cli-tests.log`；普通/生产标签 vet：`.mygo/kero-stage4-{vet,production-vet}.log`；跨层 race：`.mygo/kero-stage4-race.log`；释放隐藏图片和最终快照调整后的 race：`.mygo/kero-stage4-final-race.log`。
- 渲染截图已查看：`.mygo/kero-adoption/terminal-image-hover.png`、`kitty-terminal.png`；真实服务恢复图为 `kitty-service-reconnect.png`。iOS 静态符号表已重新生成，新增 Kitty/allocator/PNG callback 符号在当前固定 Ghostty 对象中核对存在。格式和更新说明见 [terminal-images.md](terminal-images.md)。未更新真实安装或重启服务。
- 后续 origin/滚动区域补测发现原生 formatter 的最终绝对 CUP 在 DECOM 下重复加顶部偏移，图片恢复现在按快照中的 margin 坐标补正，并跳过不透明 URI 中的控制文本；对应回归：`.mygo/kero-kitty-origin-recheck.log`。

### 第五阶段：浏览器窗格

- `desktop_browser.go` 使用 MyGo 原生 WebView，与现有 Tab/分屏树保存稳定 `web-…` ID、逻辑 URL、目录、方向、比例、焦点和放大状态。隐藏保留页面，关闭只销毁自有 WebView、取消 DOM/导航任务及端口流；浏览器排除于 PTY 轮询、尺寸接管及会话终止逻辑。
- `internal/browsercontrol` 定义有界请求和 DOM 操作，`desktop_browser_control.go` 在独立 GUI 端点上实现 list/open/status/navigate/read/click/type/wait/screenshot/close。操作按稳定身份激活目标，包括另一个窗格已放大的情况；加载/转发完成后才读取页面，导航、关闭或请求取消后丢弃迟到结果。读取同步 SPA/fragment 的逻辑 URL。
- `internal/controlcli/browser.go` 提供 `retty browser` 命令。输入经 JSON 编码、匹配唯一可用元素、执行一次；截图在后台获取实际 WebView PNG并原子保存为 0600。目录推导覆盖所属 GUI、显式参数及哈希 socket；超时小于 1ms 或超过两分钟拒绝。内嵌 Agent skill 已扩展并通过 skill-creator 校验。
- `internal/remote/ports.go` 在独立认证扩展流上连接源主机的回环端口，不调用 PTY daemon。浏览器拥有临时本机监听器及连接，按主机和 client 身份维护；HTTP、WebSocket/HMR 通过同一原始 TCP 转发。失败不回退本机，布局不保存临时本机端口。`Options.BootstrapRegion` 允许隔离中继注入，默认生产中继选择不变。
- 定向测试和边界测试：`.mygo/kero-browser-targeted-tests.log`、`.mygo/kero-browser-boundary-tests.log`；最终跨层 race：`.mygo/kero-stage5-final-race.log`。覆盖布局恢复无额外 shell、关闭不终止终端、隐藏实例保留、关闭取消 DOM、加载中读取不覆盖待加载 URL、SPA逻辑地址、放大目标、HTTP/WebSocket 和自有监听器清理。
- 真实生产 `.app` 由 `go tool mygo build` 打包并 ad hoc 签名：`.mygo/kero-browser-package-build.log`。原生验收 `.mygo/kero-browser-native-package-final.log` 通过实际 CLI、WebView、独立会话/推送进程、自有本地 DERP 和第二个隔离服务验证页面操作、截图、远端 WebSocket、GUI 正常退出重开及 URL/ID 恢复。最终记录前后 daemon PID=82928、session=d1b52cb886f4、shell PID=82935、尺寸=71×37，均不变。
- 图像已查看：`.mygo/kero-adoption/browser-native.png` 为实际页面截图，`browser-native-window.png` 为 Computer Use 读取的系统合成画面，确认终端与网页共同分屏及原生地址栏。MyGo 根绘制层 PNG 不包含原生子视图，未把黑色占位当作网页渲染证据。
- 产品接口、限制和可重跑命令见 [browser-panes.md](browser-panes.md)。远端硬编码到另一端口的子资源不会自动改写；HTTPS 证书校验保留。

## 最终逐项复核

| 项目 | 当前代码与证据 |
| --- | --- |
| Git 摘要 | `internal/gitstate`、`desktop_git.go`；分支/增删/操作状态、异步有界缓存及主机/连接隔离测试通过。 |
| Worktree 归组 | `sidebar_projects.go`、`sidebar_history.go`；common directory 归组并保留实际目录、Tab 和恢复身份；已查看工作台截图。 |
| 轮次 Diff | 独立 Git 对象/索引快照、文件通道与 `desktop_changes_view.go`；本轮/HEAD/merge-base、未跟踪文件、用户 index 不变及隔离 UI 测试通过。 |
| Agent CLI | `internal/controlcli` 与实际 CLI 进程用例；稳定身份、读写不附着、argv、退出码、超时、skill 已验证。 |
| 统一 Action | `commands.go` 与独立 GUI broker；菜单/面板/快捷键/CLI 共用注册，允许列表与禁止破坏动作测试通过。 |
| 状态报告 | `internal/rex/control.go` 复用既有 program status；层级/进度/去重/原生事件共存用例通过。 |
| 图片 | 有界主机文件预览及 Kitty 传输/VT/绘制/协商快照全链路；已查看渲染和真实隔离服务重连图片。Unicode 虚拟 placeholder 的上游限制已明确记录。 |
| 浏览器 | 原生 WebView、持久分屏与独立 DOM/截图控制，真实 Tailcat 转发的远端 HTTP/WebSocket，关闭/取消/重开及进程尺寸核对通过。 |

最终完整测试通过：`.mygo/kero-final-tests.log`（主包 82.764s）；CLI 无图形包检查通过：`.mygo/kero-stage5-cli-tests.log`；普通/生产标签 vet：`.mygo/kero-final-{vet,production-vet}.log`。最后补充放大目标显示后，定向跨层 race、两组 vet 及重新打包后的完整原生验收再次通过。MyGo fork/main/固定版本检查记录：`.mygo/kero-final-mygo-check.log`。

本机桌面及 iPhone 已覆盖安装，未发布 Release。按用户要求移除更新尺寸保护及其常驻提示，继续使用原有窗口自适应和手机尺寸接管逻辑；相关回归通过，GUI 更新后服务 PID 与六个会话的 shell PID 保持不变。网关、会话服务及现有 Agent 继续运行原实例；服务新能力继续显式协商，不能为激活新代码结束正在执行的任务。
