# 终端程序状态：OSC 7501

Retty 支持 [Program Status Protocol（OSC 7501）](https://www.superlogical.com/rex/docs/build/program-status) revision 0.3。程序通过 PTY 输出明确的状态，桌面、手机和 CLI 会话服务共用这份信息。支持这个协议的程序无需安装专用 Hook；未发送报告的 Agent 继续使用已有集成。

## 报告与能力查询

```text
ESC ] 7501 ; state=working:app=cargo:progress=40 ESC \
ESC ] 7501 ; state=blocked:app=cargo:kind=permission:msg=Q29udGludWU/ ESC \
ESC ] 7501 ; state=done:app=cargo:msg=QWxsIHRlc3RzIHBhc3NlZA== ESC \
```

`ESC` 为 `0x1b`；终止符可以是 `ESC \` 或 `BEL`（`0x07`）。上面的消息分别解码为 `Continue?` 和 `All tests passed`。`msg`、`title` 是标准 base64 编码的 UTF-8 纯文本，可省略填充。

程序可发送 `ESC ] 7501 ; ? ESC \` 查询支持，服务回复相同的固定序列。回复由拥有 PTY 的服务发送，即使没有窗口或手机连接也有效；多个查看端不会重复回复，也不会把回复记成用户输入。无需设置额外环境变量或改动系统的 `xterm-256color` terminfo。

Shell 中可以这样发送报告：

已支持会话控制的新服务也可使用 `retty report --state working --app build --progress 40` 直接报告，包含任务 ID、消息和子任务的用法见 [会话自动化](session-control.md#显式状态与-agent-skill)。这条入口与下面的终端协议共用记录及通知版本。

```sh
status() {
  printf '\033]7501;state=%s:app=build:msg=%s\033\\' \
    "$1" "$(printf '%s' "$2" | base64 | tr -d '\n')"
}
status working "Building"
make && status done "Build finished" || status error "Build failed"
```

## 状态与显示

| 协议状态 | Retty 的含义 | 生命周期 |
| --- | --- | --- |
| `idle` | 就绪 | 直到替换或清除 |
| `working` | 执行中，可显示 `0`–`100` 的进度 | 替换、清除、PTY 进程退出或下一个 OSC 133 `A` 提示符 |
| `blocked` | 等待授权、回答、登录，或其他用户操作 | 与 `working` 相同 |
| `done` | 完成 | 跨越进程退出和提示符，直到替换或清除 |
| `error` | 失败 | 与 `done` 相同 |
| `clear` | 移除记录及其子记录 | 没有 `id` 时清除整个会话的记录 |

`blocked` 的 `kind` 支持 `permission`、`question`、`auth`。没有 `progress` 表示不确定进度；`progress=0` 表示明确的 0%。手机列表沿用现有交互：不显示“已完成”和普通“等待输入”文案。

每条报告完整替换对应记录；省略的字段不会从上一条报告沿用。`id` 使用路径表达子任务，例如 `deploy/us-east`；`app` 从最近的祖先记录继承。各记录的状态独立存在，根任务就绪时，子任务仍可能等待授权。

会话服务保存全部记录，列表 API 的 `programStatuses` 返回它们，`agent` 字段提供现有 UI 和推送使用的汇总状态。当前汇总优先级为等待操作 → 失败 → 执行中 → 完成 → 就绪，同优先级选择最近更新的记录。标签页提示包含报告的标题、消息和进度；通知使用报告提供的纯文本消息。

有协议记录时，协议状态优先于专用 Hook，避免两套来源重复提醒。已接入 app-server 结构化生命周期的 Codex 根状态是例外：使用结构化事件确认完成，并将最终回复摘要作为通知文案，避免 OSC 的完成报告先到时提前发送任务标题。未接入该生命周期的 Codex、其他程序及子任务继续使用协议状态。

回到 shell 提示符仍保留完成和失败结果；开始下一条 shell 命令（OSC 133 `C`）时确认并清除旧结果和就绪记录，继续保留尚在运行或等待的子任务。清除或失效的记录不会恢复旧 Hook 状态；开始新的 Hook 会话或用户轮次后可以恢复原有集成。完整终端复位（RIS）清除所有记录；软复位和切换主屏幕 / 替代屏幕不会清除。

## 重连与提醒

状态保存在会话服务中，不需要心跳，也不因关闭窗口、断开网络或手机切入后台而丢失。查看端重连通过列表获取当前记录，不重放报告或旧提醒。只有明确的等待、完成和失败状态参与通知；不会把清除记录或回到 shell 猜成完成。

通知保留现有桌面焦点判断、手机前台静默、APNs 注册与回执机制。重复报告、消息更新和进度更新不会产生新通知；真正再次进入等待或完成才增加事件版本。同一会话、同一种提醒在两秒内最多触发一次，状态展示仍即时更新。

接收器限制整条序列为 4096 字节，每个会话最多保留 256 条记录，超出后移除最久未更新的记录。按协议检查字段长度、路径深度、base64、UTF-8 和控制字符；非法报告不会部分改变已保存的状态。界面和通知不会解释消息中的标记，也会去除不可见的格式控制字符。

## 更新与验证

能力查询和解析位于会话服务中，因此更新 GUI 或手机本身不能升级仍在运行的旧服务。安装新代码后，须在真实任务结束时再更新拥有这些会话的服务；不会为启用协议自动结束现有任务。远端 CLI 主机同样需要更新服务。

隔离测试包含分段字节流、字段边界、父子继承、清理规则、真实 PTY 能力查询、查看端重连，以及桌面 / 手机 / APNs 的去重与焦点行为：

```sh
GOWORK=off go test . ./internal/rex ./internal/push -run '^TestProgramStatus' -count=1
GOWORK=off CGO_ENABLED=0 go test -tags retty_cli ./internal/rex -run '^TestProgramStatus' -count=1
```

## 后续手动跟进协议更新

协议更新由维护者手动检查和适配，目前没有自动检测、同步或定时任务。当前实现基线为 revision **0.3**（规范修订日期 **2026-10-07**），最后核对日期为 **2026-10-10**。

更新时按以下步骤执行：

1. 查看[官方规范及修订历史](https://www.superlogical.com/rex/docs/build/program-status#revision-history)，对照当前基线阅读正文变化。重点核对语法、能力查询、状态及其生命周期、字段与继承规则、长度限制和安全要求。
2. 评估兼容性。当前解析器忽略未知字段；未知 `state` 会使整条报告被忽略，保留已有记录，不会误判为完成。新增字段在现有语法和限制内通常兼容；新增状态、查询格式、清理规则或限制变化，需要明确适配。
3. 修改项目内的接收器 [internal/rex/program_status.go](../internal/rex/program_status.go)，并按变化检查会话元数据、桌面 / 手机展示及通知逻辑。该接收器由 Retty 自行维护，不依赖 MyGo 升级来跟进协议。
4. 为新增或变更的规则补回归用例，运行上述隔离测试，并按影响范围验证重连、通知去重和 CLI 服务。测试与真实会话保持隔离，构建和安装遵循[开发说明](development.md)。
5. 同步更新代码中的协议 revision 注释、本页实现基线与最后核对日期，并记录本次适配内容。发布后升级拥有 PTY 的会话服务；有真实任务运行时保留旧服务，等任务结束再升级。涉及展示变化时同步更新桌面和手机应用。
