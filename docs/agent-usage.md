# 本机 Agent 用量

设置 → 用量展示 Codex、Claude Code 当前本机账户提供的订阅额度：周期、已用百分比、重置时间和读取时间。每三分钟后台刷新，可手动刷新；未安装 CLI、未登录、API Key 账户或读取失败时显示状态，不将缺失的额度显示为 0%。这里只显示订阅额度，不估算费用或统计聊天内容。

托盘默认显示 Retty 的 `>_` 图标和具体的剩余百分比（例如 `81%`），使用系统原生状态栏文字和等宽数字，不把数字绘进缩小的位图；两个周期以 ` · ` 分隔。设置 → 用量 →「托盘图标」可切换为 Agent 图标，跟随托盘账户显示 Codex / Claude；图标为透明单色模板，由系统适配深浅色菜单栏，修改后立即生效并保存。固定账户不可用时仍保留其 Agent 图标。图标源自仓库现有 Agent SVG，通过 `python3 scripts/generate-tray-icons.py`（需要 `rsvg-convert`）导出，运行应用无需图像转换依赖。

悬停查看周期、剩余百分比、重置时间。设置支持隐藏托盘及指定 Codex / Claude；默认优先显示有额度数据的 Codex，否则 Claude。固定账户不可用时不切换到另一个账户。没有额度数据时显示横线，与剩余 100% 区分。

点击托盘图标打开自定义概览，参考 [CodexBar](https://github.com/steipete/CodexBar) 的账户切换和额度层次：顶部用紧凑标签切换 Codex / Claude，右上角打开设置，主体显示剩余百分比、细进度条和重置时间；底部显示套餐、更新时间及刷新。切换仅预览账户，不更改托盘来源；没有数据时显示具体原因。面板高度随额度数量调整，数据异步刷新；点外部、按 Esc 或再次点击图标可关闭。macOS 使用 MyGo 原生 NSPopover 的材质、阴影和图标锚点，不打开终端窗口；命令面板也可执行「用量概览…」。右键保留概览、设置及正常退出；Linux 从托盘菜单打开同一面板。

实现参考 Magpie 的订阅额度和托盘设计，使用 MyGo 原生 Tray API。Codex 通过独立、短生命周期的 `codex app-server --listen stdio://` 读取 `account/read` 和 `account/rateLimits/read`，由 CLI 自身处理登录存储；不连接或重启现有共享 daemon。Claude 使用 Magpie 同样的 `claude -p /usage --output-format json --tools '' --strict-mcp-config --setting-sources '' --no-session-persistence`，在独立临时目录执行，读取 CLI 自身的用量命令，不创建模型对话。CLI 的账户类型和版本决定能否返回额度。

概览按周期和重置信息收紧高度，超过三个周期时滚动查看；顶部账户切换与底部刷新始终保留。剩余额度不超过 20% 时，数字和进度条同时使用提醒色，具体比例仍以文字显示。套餐以独立标签展示，长名称截断后可悬停查看。首次读取显示加载状态，失败显示完整原因（较长时可滚动）；底部区分成功「更新」与失败「检查」，跨日记录带日期。

Codex 重置卡参考本地 Magpie 的数量、到期提示和使用前确认流程，数据通过 Codex 官方 [App Server](https://learn.chatgpt.com/docs/app-server) 读取：`account/rateLimits/read` 的 `rateLimitResetCredits.availableCount` 为可用数量，`credits` 为可能不完整的卡片明细。面板和设置展示数量与最近到期时间，悬停到期提示查看逐张明细。明细不全时标注已知张数，不将其冒充完整列表；未提供该字段的旧 CLI 不显示虚构的零张，无到期限制与到期时间未知分别处理。

「使用一张…」先显示当前本机账户和消耗说明，点击「确认使用」才调用 `account/rateLimitResetCredit/consume`。完整明细中优先选最早到期的可用卡；明细不全时由 Codex 选择。提交前重新核对账户与配置目录，账户发生变化时拒绝操作。每次逻辑用卡生成一个 UUID 作为 `idempotencyKey`，失败后在本次应用运行内重试保持相同标识和卡片 ID；后台刷新不会用卡。响应丢失时保留操作以供重试，不自动另开一次消耗。确认 `reset` / `alreadyRedeemed` / `nothingToReset` / `noCredit` 后读取最新额度，额度刷新失败也不会把已确认的消费变成可重复的新操作。此功能只手动使用，不自动消耗真实重置卡。

读取遵循 `CODEX_HOME` / `CLAUDE_CONFIG_DIR`（未设置时使用用户默认目录），不扫描其他账户、不读取远端主机、不保存凭据或用量历史。GUI 的精简 PATH 会补充本机常规安装目录，供 npm 启动脚本找到 Node，不执行 shell 启动文件。单次读取最长 20 秒，单条 Codex 消息及 Claude 输出各限制为 1 MiB；Unix 上每次读取独占进程组，取消只结束本模块启动的 CLI 及其启动器子进程。关闭 GUI 窗口后 macOS 托盘继续更新，退出应用时停止。

验证使用隔离的 CLI 协议替身，覆盖只读请求、账户类型、缺失窗口、超时、Claude 输出解析、数据边界及设置持久化：

```sh
GOWORK=off go test ./internal/agentusage -race
GOWORK=off go test . -run '^TestUsage' -count=1
```
