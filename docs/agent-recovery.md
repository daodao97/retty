# Agent 会话恢复

侧边栏模式在标签页列表下方显示「项目会话」：读取本机 Codex 和 Claude Code 已保存的历史，按会话工作目录分组，隐藏 `/tmp`、`/private` 及其子目录中的项目（包括 macOS 的 `/var/folders` 临时目录别名），项目按其中最近活跃会话的时间倒序排列，项目内的会话也按最后活跃时间倒序排列。点击项目名称或箭头可展开、折叠；每条会话显示对应 Agent 图标和标题；列表仅显示图标和标题，悬停可查看账号来源、provider、完整配置目录、会话 ID 与时间。点击已打开的会话会查找所有标签页中的全部窗格（包括嵌套分屏），切换到原标签页并将键盘焦点交给对应窗格；目标被其他窗格的缩放隐藏时取消该缩放。会话未打开时才在新的本机标签页中执行精确 ID 的 resume 命令，即使当前选中的是远端标签页也不会在远端执行。识别优先使用当前生命周期会话 ID 和配置目录，并兼容显式 resume 的启动参数；旧服务没有配置目录且同一 ID 出现在多个来源时，不猜测账号；从历史启动的新会话在 hook 到达前也会去重。

历史列表不依赖 Retty 生命周期集成。它读取当前 `CODEX_HOME`、默认 `~/.codex`、有效的 `~/.codex-*` 账号目录，以及 CodexHost 账号注册表明确记录的配置目录；不扫描 `.bak` / `backup` 备份目录，符号链接别名只扫描一次。Codex 同时读取 `sessions` / `archived_sessions`、`session_index.jsonl` 和只读 `state_5.sqlite` 元数据，覆盖无 JSONL 文件的新版 paginated 会话；SQLite 使用 MyGo 的桌面组件，不增加手机端原生库。数据库提供当前名称、provider、账号归属和最后活跃时间，旧版会话仍兼容 JSONL。它也读取 `CLAUDE_CONFIG_DIR`（默认 `~/.claude`）下 `projects` 内的顶层会话文件；不显示子 Agent / sidechain。Codex 名称遵循官方 [append-only 会话名称索引](https://github.com/openai/codex/blob/main/codex-rs/rollout/src/session_index.rs)，Claude Code 使用 [CLI 的 `--resume` 参数](https://code.claude.com/docs/en/cli-reference)。点击 Codex 历史时，使用当前 `CODEX_HOME` 的登录账号和项目生效配置中的 provider 继续。Retty 通过短生命周期的本地 Codex app-server 调用原生 `config/read`；provider 或账号发生变化时调用 `thread/fork` 保留完整上下文，以新 ID 续接，原会话保留。准备过程不发起模型轮次，使用 `deferGoalContinuation` 避免继承的目标自行继续，也不重启共享 daemon。`codex-continuations.json` 以 `0600` 原子记录来源与当前配置目录 / provider / 账号对应的新 ID；重复点击和重新打开 GUI 复用分支，目标已打开时定位原标签页 / 窗格。跨配置目录时，只把原生 rollout 历史快照放入当前 `CODEX_HOME/sessions/retty-import-*`，支持 `.jsonl.zst`，不复制原账号的凭据或配置，不直接改写 Codex 数据库；原生分支前后逐轮比较完整历史，无法完整读取或校验失败时保留原会话并显示错误。快照可能被原生分页历史引用，不能手工删除。启动命令显式指定当前 provider，恢复记录只保留受限的 `model_provider`，不保存任意 `-c` 配置、凭据、提示词或历史命令。同一 ID 在不同配置目录中独立识别；账号绑定确认存在原账号副本时优先该副本。项目目录丢失时显示错误。启动前在后台解析 CLI 的绝对路径，并传入对应 PATH；当前环境找不到时读取交互式登录 shell 的 PATH，因此已运行服务缺少 `~/.local/bin` 时也能恢复，无需重启服务。命令缺失时显示错误，不创建失败标签页。

读取在后台完成，缓存未变化的文件，每 15 秒检查更新，也可点击「刷新项目会话」。每份会话只读取有界的开头和近期标题区域，元数据仅留在内存；树形列表由 MyGo `Outline` 提供虚拟渲染和展开状态。历史文件格式不兼容或损坏时跳过对应记录，未找到标题时显示会话 ID。列表只反映本机仍保存的历史，不读取另一台机器的 Agent 数据。

桌面 Retty 恢复本机保存布局中的 Codex、Claude Code 会话。需要在设置里启用对应生命周期集成，并保留 Agent 自身的会话历史；普通 shell 和其他 Agent 仍按原来的工作区恢复方式处理。

- 普通退出、重新打开 Retty：后台 PTY 存活时直接重连，复用原 PID，不重复启动 Agent。
- 电脑重启或会话服务意外退出后：重新打开 Retty，在原目录执行 `codex resume <会话 ID> -C <目录>` 或 `claude --resume <会话 ID>`，恢复对应对话并等待输入。不会使用 `--last`，也不会自动发送提示词或重放 shell 命令。
- macOS 进入关机或重启时，服务读取系统关机状态，保留因系统终止触发的 `SessionEnd`、进程退出和进程消失所对应的恢复记录；GUI 同样保留原窗格布局。无需 GUI 保持打开，也不依赖 Agent 与服务的退出顺序。主动关闭窗格和 **Quit and End All Sessions** 仍会清理记录。
- 标签页名称、分屏比例、方向、焦点和缩放沿用已保存布局。恢复请求沿用原 PTY 标识；并发打开窗口或重复重试只创建一个进程。
- 主动结束 Agent（`SessionEnd`）、正常退出恢复进程、关闭本机窗格或 **Quit and End All Sessions** 会清除恢复记录。回复完成（`Stop`）仍保留对话。未发送结束事件但已离开前台的 Agent，在确认原 Agent 进程消失后也清除记录。
- 目录缺失、CLI 未安装、历史不可用或恢复进程非零退出时保留窗格。顶部显示“恢复失败”，悬停查看原因、点击重试图标重试；CLI 输出保留在终端里。失败记录不会在每次重开窗口时自动重试。关闭该窗格会清除记录。

会话服务收到经过认证的生命周期事件后，原子写入数据目录下的 `agent-resume.json`（权限 `0600`），即使 GUI 没有打开也会记录。内容只有 Agent 类型、真实会话 ID、目录和受限的启动配置：Codex 的 model、profile、sandbox、approval、full-auto、no-daemon、受限的 model_provider，Claude 的 model、effort、permission-mode，以及 `CODEX_HOME` / `CLAUDE_CONFIG_DIR`。未知启动参数不会重放；提示词、正文、任意 shell 命令、凭据和临时 Codex relay 地址不写入该文件。禁用 Agent 历史保存的启动方式不会记录为可恢复会话。

恢复仍依赖本机安装的 Agent、已有登录状态和其保存的历史。Retty 不安装开机/登录启动项，只在用户打开桌面应用、恢复保存布局时启动 Agent。本版本不从手机或远程桌面查看端自动启动另一台机器的 Agent；远端进程存活时仍按原来的连接方式接续。

更新 GUI 不会重启正在运行的旧会话服务。新服务通过 `hello.agentRecovery` 协商此能力；旧服务继续原有重连行为。持久化记录从新版会话服务安全启动后的生命周期事件开始，不会凭空恢复之前未记录的会话 ID。有真实任务时保留旧服务，待任务结束后再更新服务。

隔离验证使用临时数据目录和假 CLI，覆盖精确会话/目录恢复、存活进程复用、并发去重、结束事件与关闭清理、失败保留、手动重试、工作区布局和旧服务兼容。关机回归注入系统关机状态，先触发结束 hook、进程消失或 PTY 退出，再让新服务读取磁盘记录并恢复原对话；同时检查 GUI 不丢失原窗格。测试不重启真实机器或向真实 Agent 输入内容。

恢复记录保留 Agent 的默认或自定义配置选择；自定义目录转为绝对路径，默认配置在启动时移除相应环境变量，旧记录中的空配置值也按未设置处理。不能把空的 `CLAUDE_CONFIG_DIR` 或显式的默认目录传给 Claude，前者会读错历史，后者会改变全局配置与目录信任设置的位置。登录 shell 的 PATH 使用带分隔标记的输出读取，忽略启动提示、OSC 目录报告和退出脚本输出；仍找不到 CLI 时检查用户的 `~/.local/bin`。服务重启后允许重新尝试先前因找不到可执行文件而未启动的会话，CLI 已启动后失败的会话仍保留错误并等待手动重试。
