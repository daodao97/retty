# 可重复的性能基线

`scripts/performance-baseline.py` 测量独立生产版 Retty 的 GUI、会话服务、推送 worker。每个场景都新建 `/tmp/retty-perf-*/data`，使用固定的 Python PTY 程序；结束时正常退出测试 GUI、关闭测试会话服务，并回收工具自己启动的进程和目录。真实应用、会话、Agent CLI、Codex daemon 不参与测量和清理。

首份标准结果见 [2026-10-13 macOS arm64 基线](performance-baselines/2026-10-13-macos-arm64.md)。

## 执行

从仓库根目录运行，macOS arm64 的标准基线为：

```sh
./scripts/check-mygo.sh
env -u MYGO_INSPECTOR GOWORK=off go tool mygo build -platform darwin/arm64 -skip-dmg .
python3 -B scripts/performance-baseline.py
```

默认每个场景预热 10 秒、采样 30 秒、每 0.5 秒读一次进程计数。运行会短暂打开测试窗口；保持机器接电、屏幕唤醒，尽量停止其他重负载工作，并在同一显示器上执行。窗口为 1000 × 620 DIP、深色、11.6 字号；截图尺寸、终端网格、窗口是否聚焦和系统 load average 都写进报告。测试 GUI 使用临时 MyGo 用户数据路径，避免读取或写回真实窗口位置与设置。

默认 `--focus-mode foreground` 通过专用 fixture 接口激活测试窗口，并在采样前后检查焦点；失去焦点则拒绝该次测量。旧二进制不支持激活接口时可以显式使用 `--focus-mode as-is`，但两种模式不能混合比较。受控基线可以仅补入相同的测试激活接口，保留补丁和构建来源。

仅用于检查工具是否可运行的短验证：

```sh
python3 -B scripts/performance-baseline.py --duration 3 --warmup 2 --skip-benchmarks
```

短验证不能当作稳定性能基线。需要更长观测或单独重测时：

```sh
python3 -B scripts/performance-baseline.py --duration 120 --warmup 20 --scenarios idle output
```

每次结果保存在新的 `build/performance/<时间>/`。`--output` 可以指定一个尚不存在的输出目录，工具拒绝覆盖已有结果。Intel Mac 先构建 `darwin/amd64`，再传 `--app build/darwin-amd64/Retty.app`。目前进程计数使用 macOS libproc，其他平台会明确退出。

## 固定场景

| 场景 | 工作负载 | 观测目的 |
| --- | --- | --- |
| `idle` | 1 个空闲 PTY，GUI 已连接，隐藏光标 | 普通空闲的 CPU、物理内存和 RSS |
| `eight_sessions` | 8 个空闲 PTY 标签页，显示第一个 | 多会话带来的服务、终端模型及轮询成本 |
| `output` | 1 个 PTY 持续输出约 1 MiB/s，带 ANSI 颜色 | 持续输出下的 CPU、内存与服务吞吐 |
| `history_search` | 生成 50,000 行含中文、颜色和固定查询词的历史，打开搜索并等扫描完成 | 搜索就绪耗时、匹配数量、搜索过程资源及搜索栏空闲成本 |
| `file_preview` | 工作目录中 100 个文本文件加 1 个 512 KiB Go 文件，打开只读官方编辑器预览 | 预览就绪耗时、预览过程资源及预览界面空闲成本 |
| `dual_viewers` | GUI 加一个本地分帧协议查看器，同时持续输出约 1 MiB/s | 同一会话分发到两个查看器的成本 |

可以通过 `--history-rows`、`--output-rate` 调整压力，但调整后的结果只与相同参数比较。持续输出达不到目标速率的 80% 时，该次测量失败；完整报告仍写出错误和已有数据。

空闲 PTY 是确定性的测试程序，不运行真实 Agent 或模型请求。双查看器只复用会话协议，不运行手机硬件、无线网络或加密隧道；真实 iPhone 的端到端性能需要另做设备基线。

## 采样口径

通过 `proc_pid_rusage(RUSAGE_INFO_V0)` 分别读取三个自有 PID；同一 PID 的 UUID 和启动时间必须保持一致，否则拒绝继续采样。

- **CPU**：累计 user/system Mach 时间转换为纳秒，以两次计数差除以实际经过时间。100% 表示占满一个核心，可以大于 100%；不使用 `ps %cpu` 的生命周期平均值。
- **物理内存**：`ri_phys_footprint`，报告中给出中位数、末次值和采样峰值。
- **RSS**：`ri_resident_size`，单独报告；它与物理内存不是同一个指标，不能混用比较。
- **唤醒**：`ri_pkg_idle_wkups` 的差值除以经过时间；这是内核计数，不能理解为全部应用定时器触发次数。
- **峰值**：离散采样最大值。启动阶段从进程创建后开始采样，等待就绪期间约每 50 ms、预热期间每 0.5 秒；短于采样间隔的峰值可能遗漏，不称为生命周期峰值。

指标依据 Apple 的 [rusage 结构](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/resource.h)及 [内核计数实现](https://github.com/apple-oss-distributions/xnu/blob/main/osfmk/kern/bsd_kern.c)。CPU 时间单位通过与 Python `process_time()` 的自有进程忙循环交叉检查。

这里只统计 GUI、会话服务和推送进程；PTY 的 Python 工作负载、采样工具和系统 WindowServer 不计入应用合计。截图发生在资源采样窗口外。测试入口在普通启动时不创建 socket、不运行后台轮询；基线模式关闭自动更新插件，避免网络检查影响重复性。

## 延迟和吞吐

每个场景在资源采样窗口之前，通过临时分帧连接执行 20 次输入回显和 10 次重新附着快照，报告原始值及 P50/P95。

回显计时从发送受控输入到收到完整回显；快照计时从建立连接到收到完整首帧。这些是本地会话协议耗时，**不包含 GUI/GPU 显示延迟**。P95 使用 nearest-rank，不插值。

搜索及预览通过 UI 主线程入口触发，控制器约每 50 ms 检查完成状态；就绪耗时包含控制器和 UI 调度。搜索状态查询也会推进终端的有界增量扫描，因此这是固定控制器驱动下的就绪指标，不能直接当作普通用户搜索的交互延迟。搜索完成要求扫描不忙且有匹配，预览完成要求官方编辑器状态已创建。生成的历史可能超过默认保留范围，实际匹配数以报告为准。该指标不是像素呈现时间。操作期间资源另存于 `operations`，稳态资源只统计完成后的 30 秒。

输出吞吐来自会话服务的实际 `SessionInfo.Output` 字节计数差，包含 ANSI 字节；它表示 PTY 已被服务消费的速率，不等同于屏幕帧率。

## 微基准和产物

工具在场景测量完成后运行现有 `BenchmarkOutput` / `BenchmarkFrame`，固定 `GOWORK=off`、生产标志、`mygo_noinspector`，每项运行 3 次、每次约 1 秒，开启 `-benchmem`。前者测量终端消费输出的吞吐；后者使用 `ui.Tester`，包括终端更新和 UI 帧构建，不能解释为原生 GPU 帧率。缺失库导致 benchmark 跳过时，应将该次结果视为缺项。

| 文件 | 内容 |
| --- | --- |
| `report.md` | 人读汇总、启动峰值、测量限制 |
| `report.json` | 完整参数、机器、提交与 dirty 状态、源码差异摘要、二进制 SHA-256、各场景结果 |
| `binary-build-info.txt` | `go version -m`，含固定 MyGo 版本和构建标签 |
| `<场景>/samples.csv` | 每次采样的原始字节计数、累计 CPU、PID、阶段和单调时钟时间 |
| `<场景>/result.json` | 场景稳态、启动及操作数据和延迟样本 |
| `<场景>/gui.png` | 测量完成后的界面截图，供检查场景是否正确 |
| `<场景>/{gui,server,push}.log` | 仅该场景自有进程的诊断输出 |
| `benchmarks.txt` | 三轮微基准原始输出 |

比较时保持机器、系统、显示倍率、窗口、字体、场景参数及预热/采样时长一致，至少独立重跑三次；结合系统负载、聚焦状态、峰值和稳定值判断。当前阶段记录事实，不设置尚无依据的性能门禁。后续优化应同时保留改动前后的完整报告，解释取舍。

## 前后对比接口

`scripts/performance-compare.py` 接受完整应用报告、Go 微基准原始输出，生成
`report.json`、`report.md` 和可筛选的离线 `report.html`。输出目录必须不存在，
已有证据不会被覆盖。stdout 返回输出目录、指标数量与是否为初步结果的 JSON，
可由其他工具调用。

```sh
python3 -B scripts/performance-compare.py \
  --before build/performance/before-01/report.json build/performance/before-02/report.json build/performance/before-03/report.json \
  --after build/performance/after-01/report.json build/performance/after-02/report.json build/performance/after-03/report.json \
  --output build/performance/comparison

python3 -B scripts/performance-compare.py \
  --before-bench before-bench.txt --after-bench after-bench.txt \
  --output build/performance/benchmark-comparison
```

按单个场景分别采样时，给对比命令加 `--split-scenarios`，并把每侧各场景的所有有效报告传入。工具按场景核对条件，按每项指标的实际重复数判断是否为初步结果；缺少另一侧的场景会报错。失焦或其他失败报告仍不能参与聚合。

两类输入也可以在一次调用中同时提供。应用报告会核对机器、负载、采样参数、
网格、窗口大小、焦点与场景覆盖；不匹配或失败的测量会被拒绝。微基准保留包名、
子基准和 CPU 并行度，并核对系统、架构和 CPU；同一路径不能重复计作多个样本。
每侧输出各轮的中位数、最小值、最大值和原始值，任一指标少于三轮则标为初步结果。
CPU 的小绝对值变化、同一进程内重复的微基准、少量请求的 P99 都不能直接视为
稳定或显著的提升。原始构建信息、二进制 SHA-256 和运行负载保留在 JSON 中。

当前优化调查与验收清单见 [性能优化调查](performance-optimization.md)。

## 真实控制接口延迟

`scripts/performance-api.py` 在独立临时目录启动指定的生产二进制，为每个会话规模创建确定性 PTY，测量真实 Unix 控制接口的 hello、完整列表、ETag 列表、新连接列表和历史读取。工具记录原始请求耗时、P50/P95/P99、传输字节、二进制 SHA 和服务构建信息，不连接已安装应用的会话。

```sh
python3 -B scripts/performance-api.py \
  --binary build/darwin-arm64/Retty.app/Contents/MacOS/Retty \
  --sessions 1 8 64 --samples 200 --warmup 20 \
  --output build/performance/api-before-01

python3 -B scripts/performance-compare.py \
  --before build/performance/api-before-01/report.json \
  --after build/performance/api-after-01/report.json \
  --output build/performance/api-comparison
```

前后使用同一版本的驱动及参数；驱动文件的 SHA 也参与条件校验。请求计时包含 Python JSON 编解码；新连接计时还包含连接建立，持久连接计时不包含。200 次请求的尾部值仅是初步观测，至少三次独立测量后再判断；这些数据不代表画面呈现、无线链路或 Go Client 独有开销。

## 实际加密画面流

`TestEncryptedStreamPerformance` 使用自己创建的会话和两端 Tailcat 身份，在真实加密连接中协商 `screen_sync`，测量首屏、约 1 MiB/s 输出和 20 次输入回显。三个场景为快速接收、1 Mbps 接收加每次更新 200 ms 处理延迟、双查看器（第二个接收端限制为 1 Mbps）。它不模拟丢包或真实手机 GPU。

```sh
RETTY_REMOTE_E2E=1 RETTY_PERF_STREAM_OUTPUT="$PWD/build/performance/stream-after-01.json" \
  GOWORK=off go test ./internal/remote \
  -run '^TestEncryptedStreamPerformance$' -count=1 -timeout=2m

python3 -B scripts/performance-compare.py \
  --before build/performance/stream-before-01.json \
  --after build/performance/stream-after-01.json \
  --output build/performance/stream-comparison
```

输出父目录需已存在，JSON 路径必须为绝对路径且尚不存在。前后复用相同夹具源码和 Go 版本，至少各三次独立进程；对比接口会核对驱动 SHA、负载参数、平台及直连/中继类型，失败或请求样本缺失时拒绝比较。原始 JSON 还保留第二查看器的解密后协议字节和重建状态字节；它们不代表无线流量。测试检查慢端持续收到更新、会话进程和网格不变，并只清理自己的服务、PTY 和临时目录。

## 工具检查

```sh
python3 -B -m unittest discover -s scripts -p 'test_performance_baseline.py'
python3 -B -m unittest discover -s scripts -p 'test_performance_compare.py'
GOWORK=off go test . -run '^TestPerformanceFixture' -count=1
```

计算测试覆盖累计 CPU 差值、不同进程分组、峰值与末值分离、P95 口径及分帧读取；完整短场景检查实际 GUI、服务和清理路径。隔离入口要求新建目录中的专用 marker、同目录 socket 和随机 token，测量命令不能连接真实会话目录。
