# 手机原生 Agent 授权

手机的授权面板使用 Agent 结构化协议；不读取终端文字，不解析菜单，不发送方向键、数字、Enter 或 Esc。未接入协议的旧会话和不支持的授权类型继续使用普通终端。

## Codex

Codex 代理默认随 Retty 终端启用，不依赖可选的生命周期 Hook。显式 `--remote`、`--no-daemon` 和工具子命令保持原连接方式。

现有 CLI → Retty app-server 代理 → 共享 Codex daemon 链路观察当前 CLI 已建立的根会话，接收 `item/commandExecution/requestApproval`、`item/fileChange/requestApproval` 和 `item/permissions/requestApproval`。手机按官方决定语义展示允许本次、会话允许、拒绝或取消；有 `availableDecisions` 时只展示其中受支持的决定。命令规则更新只使用该请求携带的原始值，不接收手机自造的规则。文件授权要求代理已收到相应的结构化文件变更预览，否则留在 CLI 处理。

每个待处理请求绑定原始 JSON-RPC ID、根会话和轮次。电脑回复、手机回复、`serverRequest/resolved`、轮次结束、会话切换和连接关闭统一撤销待处理项。电脑和手机竞争时只向 daemon 转发一个决定，晚到回复不重放。普通客户端 RPC 使用独立的 ID 命名空间，不受授权去重影响。

代理和 Hook 已运行的旧进程不会被热替换；更新后新启动的 Codex CLI 才使用新版代理。不重启共享 Codex daemon，不恢复或抢占正在运行的任务。

## Claude Code

Retty 自己的同步 `PermissionRequest` Hook 读取 `tool_name`、`tool_input` 和 `permission_suggestions`，在手机查看对应会话时等待手机选择，再从 stdout 返回官方 `hookSpecificOutput.decision` JSON。

支持允许本次、拒绝、受支持的增量权限建议（显式限制为本会话），以及“在电脑终端处理”。建议不是 CLI 按钮列表，手机不声称逐字复刻 CLI，也不暴露切换 bypass/auto 等宽泛权限模式。原生“返回终端”只关闭面板，不作出授权；“在电脑终端处理”释放 Hook 并返回 Claude 原有授权流程。

手机正在查看对应会话时每秒刷新一次短期查看租约；无查看租约的 Hook 立即返回，不增加纯电脑使用的等待。租约失效约 6 秒、接口失败或 90 秒未选择时，Hook 不返回授权决定，让 Claude 继续普通权限流程。Hook 安装超时设为 120 秒；桌面 GUI 正常启动时仅迁移 Retty 旧版自有 PermissionRequest 条目的 2 秒超时，保留用户其他配置和首次备份。旧 Claude 实例是否重载 Hook 配置由其自身决定；需要时下一次正常启动 Claude 生效，不能为此中断现有任务。CLI 主机已有的旧 Hook 需重新安装或将该 Retty 条目超时更新为 120 秒。

此 Hook 不覆盖 Claude 沙箱网络确认、任意提问或所有交互工具。没有官方结构化请求时保留终端操作。

## 传输与状态

请求只保存在生产者内存中，临时私有目录仅含 0600 Unix socket 和空的短期查看时间戳；不写工具参数、授权选项或回复到磁盘。已有 Tailcat 加密控制流增加独立 `agent-approval` 扩展。每次查询和回复都重新只读核对源服务的会话存活和 Agent 类型。有结构化根对话 ID 时继续核对该 ID；只有 OSC 7501 状态或尚无 Hook 状态时，按当前终端 SID 查询，由代理返回真实 thread ID，回复仍严格匹配该 thread、单次请求与选项。OSC 的 `osc7501:` 标记不能当作 thread ID。不向 PTY 写入，也不调整尺寸。手机只发送不可变请求 ID 和选项 ID，不能提交任意 JSON-RPC 结果或扩大权限。

同一请求自动弹出一次，标题栏“处理授权”可重新打开。切换会话、重连、进入后台或离开终端页后不提交旧面板回复。服务器仍会拒绝已处理、过期或来源不符的请求。旧网关不支持扩展时继续使用终端；更新 GUI/网关不要求升级或重启会话服务。

## 验证

```sh
GOWORK=off go test . ./internal/approval ./internal/agents ./internal/rex ./internal/remote \
  -run 'TestMobileStructuredApproval|TestApproval|TestStructured|TestHook' -count=1
GOWORK=off go test . ./internal/approval ./internal/agents ./internal/rex ./internal/remote \
  -run 'TestMobileStructuredApproval|TestApproval|TestStructured|TestHook' -race -count=1
```

原生 iPhone fixture 创建独立服务、PTY 和结构化请求，终端故意不显示编号菜单；验证取消/重开、Codex 拒绝、Claude 会话选择、零终端输入，以及会话 ID/PID 不变。

```sh
IOS_TEST_FLOW=approval IOS_TEAM=EXISTING_TEAM IOS_DEVICE=EXISTING_DEVICE \
  GOWORK=off ./scripts/test-ios.sh
```

手动签名 runner 使用设备既有的 `IOS_TEST_PROVISIONING_PROFILE` 和 `IOS_TEST_SIGNING_IDENTITY`。测试后正常启动 `com.daodao.retty`，不携带 `RETTY_UI_TEST`。

协议参考：[Codex app-server](https://learn.chatgpt.com/docs/app-server#approvals)、[Claude PermissionRequest Hook](https://code.claude.com/docs/en/hooks#permissionrequest)。当前本机 Codex 0.164.0 的 schema 也通过 `codex app-server generate-json-schema` 核对；较新字段按可选扩展处理。

已经绕过代理启动的 Codex 不会补发此前的授权请求；客户端更新不能把现有进程自动接入代理。保留当前任务，完成终端中的待处理确认后，在代理生效的终端正常重开或 resume Codex。
