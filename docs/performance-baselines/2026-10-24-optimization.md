# 2026-10-24 性能优化对照

桌面六场景、实际 Unix RPC、微基准及加密流已完成前后各三组；持续运行、原生 GPU 与跨平台构建也已完成本机检查；手机已通过 Wi-Fi 安装启动，真机性能前后对照尚未完成，本报告尚非整体完成结论。

## 构建与口径

- 基线为 `6b6adf40724e35da50f4937af573058539d6002c`，只补入相同的私有 fixture 焦点控制/日志。
- 两侧 Go 1.27.2、MyGo `27615b686564`、macOS arm64、`mygo_noinspector` 与生产标志一致。候选包含本文列出的 Retty 优化及 Tailscale 队列修复依赖。
- 每次新建隔离 GUI、服务、推送 worker 和 PTY；1000×620 DIP，实际终端网格为 142×37，打开搜索栏后为 142×35；两侧逐场景一致。预热 10 秒、测量 30 秒。
- 六场景前后交错，共 36 次有效采样；另有两次失焦被拒绝并保留。所有有效 GUI 日志中没有失焦事件。
- 每个表格值为三轮中位数；CPU 100% 为一个核心，内存为物理 footprint。范围和原始值见完整 JSON；未将中位数差异直接称为统计显著。

## GUI 稳态结果

| 场景 | CPU Before → After | 物理内存 MiB Before → After |
| --- | ---: | ---: |
| idle | 0.742% → 0.744% | 67.40 → 58.38 |
| eight_sessions | 0.775% → 0.756% | 77.99 → 60.32 |
| output | 10.389% → 9.863% | 140.56 → 140.75 |
| history_search | 5.048% → 1.673% | 143.99 → 143.90 |
| file_preview | 0.854% → 0.863% | 64.68 → 63.99 |
| dual_viewers | 10.795% → 9.124% | 140.63 → 140.92 |

静态搜索的 CPU 三轮范围分别为 5.022–5.078% 与 1.647–1.746%，完成扫描后停止无意义的 100 ms 刷新，观察到约 66.9% 的下降。空闲 CPU 基本持平；空闲与八会话物理内存中位数下降约 13.4% 与 22.6%。输出和双查看器的 CPU 中位数下降，但两侧范围存在重叠；持续输出下物理内存基本持平。预览 CPU 没有改善。

## 就绪延迟与未决项

| 操作 | Before 中位数（最小–最大）ms | After 中位数（最小–最大）ms |
| --- | ---: | ---: |
| search_ready_ms | 146.34 (110.19–408.42) | 228.36 (227.45–358.26) |
| preview_ready_ms | 96.32 (56.99–155.27) | 116.99 (64.28–174.27) |

最初三轮的搜索与预览中位数分别变慢约 56% 与 21%，但两侧范围很宽且重叠。随后另做前后各六次独立进程的就绪专用测量（预热 3 秒，完成操作后仅观测 1 秒；不与 30 秒稳态资源窗口合并）：搜索 220.54 → 231.73 ms（约 +5.1% / +11.2 ms），预览 134.76 → 132.72 ms（约 -1.5%）。未复现最初的大幅差异，搜索仍记录这个小绝对值的增加，不宣称交互延迟改善。

控制器约每 50 ms 检查一次，搜索状态检查本身也推进增量扫描；这些值包含 UI 调度、检查周期和环境波动。匹配数两侧均为 8,126。原始六组就绪报告在 `build/performance/final-ui-ready-20261024/`，资源测量及就绪测量的预热/采样条件分别保存，未混合计算。

## 真实 Unix RPC（三组 × 每项 200 请求）

| 会话数 | hello P50 µs Before → After | 完整列表 P50 ms Before → After | 缓存列表 P50 ms Before → After |
| ---: | ---: | ---: | ---: |
| 1 | 23.688 → 19.791 | 0.065 → 0.063 | 0.059 → 0.058 |
| 8 | 24.208 → 20.417 | 0.307 → 0.313 | 0.286 → 0.283 |
| 64 | 32.979 → 19.666 | 2.376 → 2.385 | 2.178 → 2.178 |
| 256 | 23.562 → 18.167 | 10.577 → 10.461 | 9.724 → 9.653 |

这是指定生产二进制与 Python 驱动之间的实际 Unix RPC，包含 Python JSON 编解码；它不经过 Go 客户端 reader。hello 中位数下降数微秒，完整/缓存列表中位数基本持平，没有把 Go 客户端分配下降误称为服务端列表延迟的大幅改善。完整 80 项指标（含 P95/P99、连接与历史读取）在 `build/performance/final-followup-20261024/api-comparison/report.{json,md,html}`。

## 最终 Go 1.27.2 微基准（三组独立进程）

| 工作负载 | ns/op Before → After | B/op Before → After |
| --- | ---: | ---: |
| terminal/BenchmarkFrame-8 | 1,441,335.0 → 1,448,777.0 | 499,055 → 39,649 |
| terminal/BenchmarkFrameWorkload/single-cell-8 | 49,915.0 → 48,856.0 | 5,544 → 3,157 |
| terminal/BenchmarkFrameWorkload/unicode-8 | 354,236.0 → 326,756.0 | 103,922 → 17,740 |
| rex/BenchmarkControlConnection-8 | 80,732.0 → 17,747.0 | 1,057,835 → 70,547 |
| rex/BenchmarkLiveServerList/sessions=64-8 | 1,161,345.0 → 1,169,365.0 | 562,945 → 387,177 |
| files/BenchmarkDirectoryList/entries=5000-8 | 19,576,960.0 → 14,388,063.0 | 5,809,520 → 4,670,843 |
| gitstate/BenchmarkRepositoryStatus/files=1000-8 | 75,732,881.0 → 65,525,289.0 | 177,759 → 161,051 |
| runtimeinfo/BenchmarkCurrent-8 | 555.5 → 6.5 | 672 → 0 |

软件帧构建的分配下降约 92%，分配次数 2,220 → 738，但耗时中位数约 +0.5%，基本持平，不能再把较早 Go 1.27.1 微基准的耗时改善当作最终结论。Unicode 与单格更新的分配下降；图片替换分配与耗时基本持平。

已知取舍：256 会话的热缓存列表客户端微基准约 +4.2%（21.612 → 22.529 µs）；单会话服务端列表微基准约 +5.3%（17.365 → 18.279 µs）。实际 RPC 列表延迟基本持平。较小常驻 reader、减少分配改变 GC 的工作点；保留内存收益，同时记录这些小绝对值的耗时增加。未改动的 4 MiB 范围读取初次约 +8.5%，独立进程控制复测约 +3.8%，分配数不变，不能把所有耗时差都归因于代码。

完整 91 项微基准指标在 `build/performance/final-followup-20261024/bench-comparison/report.{json,md,html}`；每侧三次调用测试二进制，SHA 和构建信息在运行清单。

## 实际加密画面流

三个场景（快速接收、1 Mbps 加 200 ms 处理延迟、双查看器）的前后各三组均通过；全部为直连，协商 `screen_sync`。慢端持续消费更新，会话进程和 120×38 源网格保持，约 1 MiB/s 的源输出未被慢查看器阻塞。完整 15 项对照在 `build/performance/final-followup-20261024/stream-comparison/`。

`candidate-03` 的首屏协议耗时稳定约 400 ms，直接原因是只读流未调用 Resize 时也等待初始附着计时器。追加的 `candidate-04` 在首次读取时异步启动一次附着；读取方在拨号中关闭时仍会及时返回。旧代码的回归先按预期失败，新代码连续 10 轮通过，随后全量测试、两种构建标签的 vet、主包/rex/remote 竞争检查及生产构建全部通过。

`candidate-04` 的实际加密复测也完成各三组，三种接收场景的首屏中位数分别为 **1.44 / 1.36 / 2.04 ms**，基线为 402.66 / 402.17 / 403.92 ms。减少约 99.5–99.7% 的已连接只读流首帧协议等待；这不包含建立隧道、原生绘制或屏幕显示。输入回显仍约 16 ms，所有用例检查源会话进程、120×38 网格及协商协议保持正确。两组对照分别在 `build/performance/viewer-attach-20261024/comparison-{original,candidate03}/`。

资源与微基准表格对应冻结的 `candidate-03`；追加只读附着优化对应 `candidate-04`（SHA-256 `3612e5660b90cdcad181f31ef2615d3972a4d68a43caf16fc00680db4fa32df6`）。未把不同二进制混在同一侧聚合。接收端限速发生在解密后，不代表无线丢包或原生像素显示延迟。

## 持续运行、原生诊断与平台检查

前后 180 秒输出均通过，实际速率约 1,048,248 / 1,048,366 B/s。GUI 首末四分位 footprint 中位数为 140.60 → 141.15 MiB（before）、141.91 → 152.28 MiB（candidate-04）。候选约 10 MiB 的阶跃发生后，末段趋于稳定；这次数据没有显示输出场景的内存改善。

另外以同一候选运行 180 秒并采集自有 PID 的 VM map，未复现超过 6 MiB 的持续阶跃，footprint 约 139.4 → 143.0 MiB，IOSurface 驻留初末均约 29.1 MiB。没有证据把先前阶跃归因于某个 drawable，也未把一次阶跃定性为泄漏。这是短期持续负载诊断，未宣称长期内存必然不增长。原始数据分别在 `final-platform-20261024/soak-*` 和 `memory-step-20261024/`。

原生 Metal 另用每批变化的八位计数输出，前后各三次实际记录。只保留各自 GUI PID 的呈现反馈事件，排除同机正常 Retty 的事件；稳定窗口 2–9 秒的回调频率分别为 12.57 / 13.71 / 13.71 次每秒和 14.14 / 15.00 / 13.71 次每秒。该工作负载约每秒发出 16 批数据；范围重叠，未见稳定下降，但没有将反馈回调数或 CPU 帧耗时称为显示器 FPS，也不宣称 GPU 帧率提升。原始状态码未作未文档化解释，证据为 `final-platform-20261024/metal-owned-callbacks.json` 及对应 trace/XML。

CLI 标签下四个包的测试、Linux amd64 编译通过。前后 iOS 包均使用 Go 1.27.2 和发布的 MyGo 固定版本，MyGo 架构、metadata、隐私、符号和严格签名检查全部通过；Team、应用标识、profile、证书和 Keychain 组沿用已有配置。构建与签名清单为 `final-ios-20261024/manifest.json`。构建阶段未安装或上传；后续 Wi-Fi 安装见下文，真机性能对照仍未完成。

对比页面已在 Safari 实际加载，输入 `first_viewport_ms` 后准确显示三条数据，清空后恢复回显和吞吐指标；截图与记录在 `viewer-attach-20261024/comparison-original/ui-{verified.png,verification.json}`。

## 证据与复现入口

- 原始桌面记录：`build/performance/final-20261024/manifest.json`，含全部 38 次尝试和有效报告路径。
- 完整 292 项对照及离线筛选页：`build/performance/final-20261024-comparison/report.{json,md,html}`，每项都是 3 对 3。
- 后续运行清单：`build/performance/final-followup-20261024/manifest.json`，以实际退出码与报告为准。
- 构建来源：`.mygo/perf-20261024/{before-go1272,candidate-03}/provenance.json` 和对应 `change.patch`。
- CLI、JSON/HTML 对比及各夹具命令见 [性能工具](../performance.md)，实验台账及未完成清单见 [优化调查](../performance-optimization.md)。

软件帧微基准、控制协议回显与原生 GPU 呈现是不同口径。当前表格不是手机性能结论；后续 Mac 和手机安装状态如下。

后续安装：Mac GUI 已更新为 candidate-04；会话服务 / shell / Agent PID 保持。一个会话尺寸由 48×33 变为 184×57，差异与备份保存在 `.mygo/maintenance/20261024-195833-install/verification.json`，未宣称尺寸完全一致。

后续手机安装：Xcode 已识别已配对在线的 iPhone 16，通过 Wi-Fi 成功覆盖安装并正常启动 candidate-04；复用原 Team、Bundle ID 和 profile，未执行卸载。回执在 `.mygo/maintenance/20261024-203018-phone-install`。手机已安装不等于完成真机性能前后对照，后者仍未验收。
