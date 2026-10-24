# Hook 与版本诊断

## Hook 与版本诊断

通过设置 → 诊断、Retty 菜单或命令面板的「Hook 与版本诊断…」打开。检查对象是当前窗格所在主机，选择 codex、claude、gemini 或 qwen 后刷新；远端断开或网关太旧时显示错误，不使用本机配置代替。

页面显示 GUI、会话服务、诊断进程/网关的构建版本、revision、MyGo 依赖、服务 PID 和协议。服务与 GUI 构建不同时提示差异，不启动更新或重启任务。旧服务没有构建信息时显示未提供，协议版本不当作应用版本。

工作目录优先使用服务最近接受的 Agent 生命周期事件 cwd；缺失时使用会话目录，并标明来源。新服务还公布创建会话时注入的 RETTY_HOOK 路径，避免把更新后的 GUI 路径误认为旧会话的环境。旧服务只能从服务执行路径推断，页面明确标注；之后在 shell 中手工修改环境不会被追踪。

只读检查包括：

- 工作目录是否存在、是否为目录，兼容指向新目录的符号链接。
- 所选配置目录内的 hooks.json（Codex）或 settings.json（其他集成）的格式及 command Hook。
- 简单命令的执行文件，以及 sh/bash/zsh/python/python3/node 的直接脚本参数是否存在、是否具有必要权限。
- Retty 环境变量 Hook 的会话注入路径。其他复合命令、变量展开、shell 函数等显示为动态命令，不尝试执行。

配置最大 1 MiB，最多检查 128 条命令，每条最多 4096 字节。命令名通过诊断进程的 PATH 解析，并提示它可能与 Agent 的 PATH 不同。系统 /bin/sh 仅作为参考，不声称它就是 Agent 使用的 Shell。

检查范围不包含项目配置、内联 TOML、插件和托管 Hook。最近接受的生命周期事件证明对应事件到达 Retty，不证明其他 Hook 成功。Agent 在启动 Hook 之前发生的 ENOENT 不会产生 Retty 事件，页面不会虚构运行失败记录；需结合 Agent 的报错和红色路径检查判断。

刷新取消前一次检查，关闭设置取消未完成请求；源会话关闭或主机连接变化后丢弃旧结果。远端读取走独立认证扩展流，不要求更新或附着 PTY 服务。复制诊断只写入本机剪贴板，包含配置路径和 Hook 命令，不附带会话提示词、Agent 回复摘要或终端文本；分享前可自行检查命令内容。

## 隔离验证

```sh
MYGO_TEST_IMAGES="$PWD/.mygo/kero-adoption" GOWORK=off go test . \
  ./internal/diagnostics ./internal/remote ./internal/rex ./internal/terminal \
  -run 'TestDiagnostics|TestDiagnose|TestHostDiagnostics|TestRecentOutput|TestAgentDiagnostics' -count=1
```

原生浏览器验收也覆盖诊断：隔离服务启动真实 HTTP/WebSocket 页面，经 CLI 手动打开浏览器；独立配置中的缺失 Hook 在诊断页被检出。随后验证浏览、输入、截图、远端页和 GUI 重开，核对服务 PID、会话 ID、shell PID、终端尺寸不变。运行方式见 [浏览器窗格](browser-panes.md)。测试诊断探针只在带私有标记及认证 token 的既有 fixture 端点中启用。

终端“开发地址”栏、自动发现扫描和设置开关已按用户要求移除。下方保留当时的历史验收记录，不代表当前仍提供该入口。

### 2026-10-23 验收记录

- 完整 Go 测试通过：`.mygo/diagnostics-devserver-complete-tests.log`，主包 69.768s。
- 无图形 CLI 测试通过：`.mygo/diagnostics-devserver-final-cli.log`，覆盖 CLI、诊断、服务、网关和屏幕包。
- 跨层 race 通过：`.mygo/diagnostics-devserver-complete-race.log`；普通与生产标签 vet 通过：`.mygo/diagnostics-devserver-complete-{vet,production-vet}.log`。
- 修正并复核后台发现对空闲 UI 的影响、设置分组位置和 GUI 控制队列截止时间。截止时间用例连续 25 次通过：`.mygo/diagnostics-devserver-deadline-recheck.log`。执行路径符号链接规范化后的定向复核：`.mygo/diagnostics-devserver-path-recheck.log`。
- MyGo 生产 `.app` 打包与 ad hoc 签名通过：`.mygo/diagnostics-devserver-package.log`；最终原生验收通过：`.mygo/diagnostics-devserver-native-verified.log`（11.768s），包含真实 Tailcat 加密流上的独立远端 Hook 配置读取。
- GUI 正常退出重开前后，daemon PID=97328、session=f7e23a2452ec、shell PID=97335、终端尺寸=71×35，均不变。测试仅拥有并清理其独立数据、进程和本地中继。
- 已查看界面截图：`.mygo/kero-adoption/hook-version-diagnostics.png`、`development-addresses.png`；系统合成画面 `development-browser-native-window.png` 确认终端地址入口和实际原生网页共同显示，并通过 Computer Use 点击「打开」确认复用现有窗格。
- MyGo 固定 fork/main 检查通过：`.mygo/diagnostics-devserver-mygo-check.log`。未修改真实 Hook 配置、覆盖安装应用、发布或重启真实后台服务；原有未提交改动保留。
