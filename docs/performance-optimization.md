# Retty 性能优化调查

本轮代码优化、本机前后对照与跨平台构建已完成。随后按用户要求安装并重开了 Mac GUI，后台服务及任务进程保留；手机也已通过 Wi-Fi 覆盖安装并正常启动。实际 iPhone 的性能前后对照尚未完成，当前不能把整体性能任务标为完成。

面向结果的表格、回退与测量限制见 [2026-10-24 对照报告](performance-baselines/2026-10-24-optimization.md)，可复用的 CLI、JSON 与离线 HTML 接口见 [性能工具](performance.md)。

## 测量来源

所有主对照使用同一机器、Go 1.27.2、发布的 MyGo fork `27615b686564`、生产标志和 `mygo_noinspector`。GUI 为 1000×620 DIP，实际网格 142×37，打开搜索后为 142×35。每次使用新建数据目录、服务、PTY 和连接身份，不连接真实会话或共享 Codex daemon。

| 冻结构建 | 来源 / SHA-256 | 用途 |
| --- | --- | --- |
| before-go1272 | `6b6adf40724e35da50f4937af573058539d6002c`，只补入相同 fixture 焦点控制；`9343e0b285f1171f135c9da3f5ce2cfeb7537010c0bdf82a8c0a1cc8cda5f8ba` | 同工具链基线 |
| candidate-03 | `de2a03e1263271cb5b0919fc0e3bc54ce19349242a580524b2a26327768dcc61` | 原生六场景、控制 RPC、微基准及初次加密对照 |
| candidate-04 | `3612e5660b90cdcad181f31ef2615d3972a4d68a43caf16fc00680db4fa32df6` | 追加只读附着优化；加密首帧、持续运行及动态输出 Metal 诊断 |

构建、源码差异、二进制和签名信息保存在 `.mygo/perf-20261024/`。同一对比报告的每一侧只包含一个冻结二进制，没有把 candidate-03 与 candidate-04 混为一个版本。较早 Go 1.27.1 的探索结果保留，但不代替最终 Go 1.27.2 数据。

## 覆盖与证据

以下路径均相对于 `build/performance/`。

| 方向 | 已完成证据 | 当前结论 |
| --- | --- | --- |
| 启动、空闲、多会话 | `final-20261024/`、`final-20261024-comparison/`；六场景前后各三组，36 有效 / 38 次尝试，292 项指标 | 有效 GUI 日志无失焦。空闲 CPU 持平；空闲 / 八会话 footprint 中位数下降约 13.4% / 22.6% |
| ANSI、Unicode、局部更新、图片、搜索 | `final-followup-20261024/bench-comparison/`；三组独立进程，91 项指标 | 软件全帧分配下降约 92%，耗时基本持平；图片替换基本持平，不称为 GPU FPS 改善 |
| 控制协议 | `final-followup-20261024/api-comparison/`；1/8/64/256 会话，各三组、每项 200 请求，80 项指标 | hello 中位数减少数微秒；完整 / 缓存列表的实际 RPC 中位数基本持平 |
| 实际加密链路 | `final-followup-20261024/stream-comparison/`、`viewer-attach-20261024/comparison-original/` | 三种接收场景各三组，均为直连；慢端持续消费、源输出未阻塞，PID 与 120×38 源网格保持。追加只读首帧约 403 → 1.4–2.0 ms |
| 文件、Git 与所有权 | Go 回归及最终微基准，`viewer-attach-20261024/range-control-comparison/` | 目录排序与 Git main 分支检查获益；大响应、返回列表副本、进程目录字符串所有权及取消契约保持 |
| 搜索 / 预览就绪 | `final-ui-ready-20261024/comparison/`；前后各六组，122 项指标 | 搜索约 +11.2 ms、预览基本持平；未复现初次三轮的大幅差异，保留两批数据与条件 |
| 原生 GPU 与 CPU | `final-platform-20261024/metal-*`、`metal-owned-callbacks.json`、`cpu-profile-*` | 动态计数输出实际采集 Metal 呈现反馈，按测试 PID 过滤。三组反馈节奏范围重叠，未见稳定下降；不将回调数或 CPU 帧耗时称为显示器 FPS |
| 持续运行与清理 | `final-platform-20261024/soak-comparison/`；前后各 180 秒；另有 candidate-03 持续运行及 3×5,000 文件连接审计 | 两侧约 1 MiB/s，测量及清理通过。候选一次出现约 10 MiB 阶跃后趋稳；额外 VM map 诊断未复现该阶跃，未定性为泄漏或某个 GPU 缓冲 |
| CLI / Linux | `final-platform-20261024/{cli-tests,linux-cli-compile}.log` | CLI 标签下四个包测试及 Linux amd64 编译通过；未宣称 Linux 运行时性能已实测 |
| iOS | `final-ios-20261024/manifest.json`、两侧 `BuildReport.json` | 同 Go 工具链的前后包构建、严格验签和 MyGo 检查通过；既有签名配置一致。随后已通过 Wi-Fi 安装启动，尚未完成真机性能对照 |
| 对比展示 | `viewer-attach-20261024/comparison-original/ui-verification.json`、`ui-verified.png` | Safari 实际加载，筛选后显示三条首帧数据，清空后恢复回显与吞吐指标 |

## 采用的优化与取舍

| 改动 | 最终证据 / 约束 |
| --- | --- |
| 行字形及行构建 scratch 复用 | 全帧 499,055 → 39,649 B/op，2,220 → 738 allocs/op；耗时中位数约 +0.5%，基本持平。像素一致性覆盖样式、增长、缩短、空行、中文、组合字符与 emoji |
| 限制缓冲保留 | scratch 的 rune/列/宽度上限 8,192，样式上限 1,024；清除已删除 run。宽行缩成短行后释放超额 glyph/列容量，回归先复现 12,083 个槽位保留，再验证修复 |
| 控制 reader 1 MiB → 64 KiB，ReadSlice + 大行复用 | 建立连接约 1,057,835 → 70,547 B/op，80.7 → 17.7 µs。无新增消息长度限制；1.8 MB 分片 Unicode 回复及下一条回复通过 |
| 搜索扫描结束后停止 100 ms 刷新 | 搜索稳态 GUI CPU 5.048% → 1.673%；虚拟时钟证明完成后无持续请求，新增输出与导航仍更新 |
| 目录排序预计算小写名称 | 5,000 项约 5,809,520 → 4,670,843 B/op，104,305 → 42,549 allocs/op；保持目录优先、Unicode 小写和原名称 tie-break |
| 构建信息只读缓存 | Current 约 555.5 → 6.47 ns/op，672 → 0 B/op；Version 仍读取入口稍后设置的值，不共享可变返回对象 |
| libproc 目录临时缓冲池 | 64 会话约 562,945 → 387,177 B/op；仍实时读取当前目录，返回字符串独立，长短目录及并发竞争检查通过 |
| Git 已提交 main 的默认分支检查 | 避免一次冗余子进程；1,000 文件约 75.7 → 65.5 ms，保留 origin/HEAD、unborn、master、topic 和 detached 回退 |
| 文件连接关闭顺序与 deadline 清理 | 请求方解码完先关闭，保持 JSON / 旧客户端兼容。修复后的实际加密读取三组各 5,000 次通过；释放夹具引用和两次 GC 后保留堆约 18.49 → 4.14 MB |
| 只读流在首次读取时异步附着一次 | 消除无布局待收集时的 400 ms 等待；尺寸所有者走原流程。旧回归预期失败、新回归 10 轮通过；关闭在途拨号时读取及时返回。实际加密三组首帧约 403 → 1.4–2.0 ms |

明确保留的取舍：256 会话热缓存列表客户端微基准约 +4.2%（+0.9 µs），单会话服务端列表微基准约 +5.3%（+0.9 µs）。实际列表 RPC 基本持平。早期有界关闭 GC 诊断支持常驻 reader 减小后 GC 工作点变化的解释；没有把关闭 GC 作为产品设置。调用者可修改返回列表，因此保留副本契约。

4 MiB 范围读取未改动，其微基准初次约 +8.5%，独立进程控制复测约 +3.8%；分配数不变。这是未归因的小耗时差，不把所有正负变化都称为代码收益。详细范围与原始值保留在报告中。

## 依赖与被否定的实验

- 仅把 reader 缩到 64 KiB 时，大列表分配曾增加约 13%，未单独采用；补入小回复借用、大回复复用后再验证。
- 256 会话缓存列表较早测得约 +7.4%，默认 GC / 有界关闭 GC、交错独立进程与最终实际 RPC 分别复查，结果和取舍均保留。
- 旧 Tailscale 在实际文件请求第 337 次超时，一次清理在 queue.Write 挂起；对确认属于测试的进程采集堆栈并清理，回执在 `.mygo/perf-20261024/before/diagnostic-cleanup.json`。
- 固定到包含 [上游队列死锁修复](https://github.com/tailscale/tailscale/commit/3c0508fa283fb759afc7e2360aeb293520233155) 的 `v1.105.0-pre.0.20261013225543-3c0508fa283f`，最低 Go 变为 1.27.2，MyGo 未变。仅升级后仍在第 561 次超时，故继续修复应用层关闭顺序；没有把两类问题混为同一原因。
- 一次新增 Windows 间接依赖的代理归档与 sum.golang.org 不符，Go 拒绝；改用独立缓存、官方代理及相同校验服务得到匹配归档，再完成升级，未放宽校验。记录在 `experiment-tailscale-queues/{upgrade.log,verify-windows.log,upgrade-verified-proxy.log}`。
- 首轮焦点不同的前后数据、后续失焦尝试均被拒绝。原始错误与截图保留；正式有效样本单独聚合。
- 第一次 180 秒基线失焦，后续独立补测通过；未把失败数据纳入对比。
- 一次原生 CPU 采样收尾时 GUI socket 消失，未作性能估计；后续独立前后重跑完整通过。
- iOS 基线第一次因整个缓存目录的符号链接让临时构建配置落到父模块而失败；改为配置目录独立、仅共享 Zig/Ghostty 缓存后，两侧构建与验签通过。

## 检查与剩余工作

最后一项产品代码改动（按读取附着）后的 `GOWORK=off go test ./...`、两种标签的 vet、主包/rex/remote 竞争检查、`check-mygo.sh`、生产构建及实际加密三组复测均通过。20 项 Python 工具检查通过。

全量测试曾遇到历史会话激活的异步断言偶发失败：该路径即使复用既有 Codex tab 也异步解析。测试改为等待实际激活，保留原有目标 tab / 本地 Host / 数量断言；定向 50 轮、全量和竞争检查随后通过。

当前剩余项是实际 iPhone 的前后安装包对照：画面呈现、滚动、前后台恢复、远程输入与同步。已准备的包在 `.mygo/perf-20261024/{before-ios-go1272,candidate-04-ios}/`，复用既有 Team、Bundle ID 和 provisioning profile。设备自动化控制服务尚未就绪；这与 Xcode 经 Wi-Fi 安装启动应用是两条独立路径。未以编译通过、模拟或 Mac 上的加密测试替代真机验收。

完成仍需实际设备证据。恢复后只使用自建会话、测试配置和既有签名，结束时启动无测试参数的正常应用并清理自有测试资源，不结束真实 daemon、PTY、Agent CLI 或共享 Codex daemon。

## 后续 Mac 安装

用户要求安装后，将已验证的 candidate-04 原子替换到 `/Applications/Retty.app`，资源与配置逐文件校验一致，严格签名通过，并正常重开 GUI。原 daemon、会话、shell 及受保护后台 / Agent 进程保持。当前会话尺寸从安装前快照的 48×33 变为重开后的桌面 184×57，其余会话尺寸保持；尺寸差异已记录，未通过服务重启或向终端输入来修复。备份及验收记录在 `.mygo/maintenance/20261024-195833-install/`。服务端优化需等安全维护时再启用新后台代码。

后续手机安装：Xcode 已识别已配对在线的 iPhone 16，通过 Wi-Fi 成功覆盖安装并正常启动 candidate-04；复用原 Team、Bundle ID 和 profile，未执行卸载。回执在 `.mygo/maintenance/20261024-203018-phone-install`。手机已安装不等于完成真机性能前后对照，后者仍未验收。
