# 会话与 GUI 自动化

桌面可执行文件与无图形界面的 `retty` CLI 共用会话控制命令。在 macOS 正式桌面应用中按 `⌘⇧P`，搜索 **Install CLI** 并执行，或使用 Retty 菜单的同名入口，将 `/usr/local/bin/retty` 链接到应用内的可执行文件。目录需要权限时弹出系统管理员授权；取消不安装，同名文件或其他目标的链接不会被覆盖。应用在原位置更新后，命令继续使用新版。

安装后使用 `retty session help` 查看完整参数。没有安装 PATH 入口时，在 Retty 创建的终端里可用 `"$RETTY_HOOK"` 替代下面的 `retty`。此入口提供桌面会话和浏览器控制；服务器的 `serve/link/update` 命令仍来自独立 CLI 包。

浏览器使用独立的 `retty browser` 命令与持久 `web-…` ID，支持读取、点击、输入、等待、截图和远端开发页，详见 [浏览器窗格](browser-panes.md)。

## 选择与读取

```sh
retty list --json
retty session status SESSION_ID --json
retty read SESSION_ID --lines 200 --json
```

会话 ID 在该会话存续期间不变；允许至少四字符的唯一前缀，歧义时报错。省略目标时使用 `RETTY_SESSION`，不会猜测当前 GUI 焦点、标题或列表序号。`list` 不要求目标，`session status` 无目标且无环境身份时列出全部会话。原有 `retty status [--data-dir PATH]` 仍输出网关与会话服务信息。

终端内通过 `RETTY_AGENT_SOCKET` 选择所属服务，`RETTY_DIR` 选择所属 GUI 数据目录。外部脚本可指定 `--data-dir PATH` 或 `--socket PATH`；普通 `<data-dir>/server.sock` 同时确定 GUI 目录，长路径使用临时目录中的哈希 socket 时，GUI 操作须提供 `--data-dir`。CLI 的外部默认目录是 `RettyServer`，桌面可执行文件的默认目录是 `Retty`。这些读取命令只连接现有服务，不创建、重启或替换 daemon。

`read` 通过独立、有界的历史连接读取，默认显示纯文本；`--json` 返回 `id`、`text`、`output`、`revision`、`cols` 和 `rows`，`--ansi` 可显式取得 VT 快照。`--lines` 范围为 1–8192，快照最多 8 MiB。读取不会附着终端或取得尺寸控制权。

## 输入、新任务与等待

```sh
retty send SESSION_ID --text 'literal input' --enter --if-output 123
printf 'first line\nsecond line' | retty send SESSION_ID --stdin
retty send SESSION_ID --key ctrl-c
retty new --dir /absolute/project --json -- go test ./...
retty wait NEW_SESSION_ID --until exit --exit --timeout 5m --json
```

先读取并核对终端，再执行已授权的输入。`--text`、`--stdin` 和 `--key` 三选一；支持 enter、tab、escape、ctrl-c、ctrl-d。`--enter` 额外附加一次回车，总输入限制 64 KiB。`--if-output` 对照上次读取的 `output`；发生变化时拒绝输入，要求重新读取。服务独立写入一次，不附着、不调整终端尺寸；连接错误或部分写入不会自动重试，结果不确定时应先读屏再决定。

`new -- COMMAND ARG...` 直接启动该进程，保持 argv，不经过字符串拼接或隐式 shell。默认工作目录为调用者目录，默认尺寸 80×24；可用 `--dir`、`--cols`、`--rows` 修改。新会话返回完整 SessionInfo（JSON 中的 `id`），GUI 的正常轮询会发现它；不会改变其他会话的焦点或尺寸。

`wait --until exit --exit` 返回该直接进程的真实退出码；信号退出按 128 加信号号返回。超时退出码为 124，取消为 130。`--until idle|done|blocked` 可等待 shell 提示符、明确的 Agent 完成/失败或等待输入状态，进程退出也结束等待。等待限时默认五分钟，最大 24 小时。向已有 shell 发送的命令行没有独立的进程退出记录；需要退出码时使用 `new -- COMMAND`。

## 分屏与 Action

```sh
retty split SESSION_ID --direction down --json -- /bin/sh
retty actions --json
retty action pane.zoom --target SESSION_ID --json
```

这些操作需要对应数据目录中的桌面 GUI。GUI 使用独立的本机 socket（0600），同一目录只允许一个拥有者，不替代会话 daemon。分屏返回新会话的 `sid`，保持父窗格的会话身份；如果创建成功后窗口关闭，错误中提供新会话 ID，可通过 `list` 找回，不自动终止该进程。

菜单、快捷键、命令面板与 CLI 使用相同的命令注册对象和稳定 ID。`actions` 返回每个 ID、标题、快捷键及 `allowed`。CLI 只执行明确允许的动作；关闭、重启、结束所有会话、清屏和更新保持 GUI 边界，不能通过 CLI Action 绕过。需要目标的动作必须指定会话身份。GUI 队列在执行前检查取消和窗口身份，关闭窗口会取消自有端点及等待请求。

## 显式状态与 Agent skill

```sh
retty report --state working --id build --app builder --progress 0
retty report --state blocked --id build/tests --kind question --message 'Choose a target'
retty report --state done --id build/tests --message 'Tests passed'
retty report --state clear --id build
retty skill
```

状态、进度、子任务、字段限制、通知版本与去重沿用 [OSC 7501](program-status.md)，直接报告不会干扰正在分段接收的终端转义序列。`working/blocked/done/error/idle/clear` 支持与原生事件共存；报告替换对应整条记录，`clear --id PARENT` 删除它及其子任务。不要把报告动作当成底层任务已完成的证据。

`retty skill` 输出随可执行文件内嵌的 [retty-control SKILL.md](../internal/controlcli/skills/retty-control/SKILL.md)，`--json` 返回名称和正文，方便按 Agent 的安装方式保存；命令本身不修改全局 Agent 配置。没有服务时也可读取 skill 和帮助。

输入与直接报告要求服务在 Hello 中公布 `session_control`。旧服务会收到清晰的升级提示，不自动重启；有任务时保留服务，待任务结束后再更新。安装 GUI 本身不会给运行中的旧服务启用这些新操作。

## 隔离验收

```sh
GOWORK=off go test . ./internal/controlcli ./internal/desktopcontrol ./internal/rex \
  -run '^(TestControl|TestDesktopControl|TestExplicitProgram|TestArguments|TestResolve|TestAutomationRead|TestSendPreserves|TestEndpoint|TestOldServer|TestWaitDeadline)' -count=1
GOWORK=off CGO_ENABLED=0 RETTY_CLI_E2E=1 go test -tags retty_cli \
  ./cmd/retty -run '^TestCLIControlProcessExitCodes$' -count=1 -timeout 2m
```

测试只启动临时服务和会话，覆盖实际主进程退出码、读写不附着、不接管尺寸、过期输出拒绝、旧服务无输入、报告去重、子任务清理、端点取消及 GUI 真实分屏。布局图位于 `.mygo/kero-adoption/cli-created-splits.png`。
