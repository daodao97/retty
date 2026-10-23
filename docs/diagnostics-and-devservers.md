# Hook 诊断与开发地址发现

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

## 开发地址发现

终端输出例如 `Local: http://localhost:5173/` 后，窗格上方出现「开发地址」。选中地址点击「打开」，在该终端旁打开浏览器；重复点击复用同一主机、同一页面，包括 WebView 加载后补上根路径 `/` 的情况。

支持 http/https 和省略 scheme 的显式端口地址，识别 localhost、127.0.0.0/8、`[::1]`。服务器打印的 `0.0.0.0`、`[::]` 转换为可访问的 localhost。不同 scheme 保持区分；最近出现的同源地址优先，最多显示八个。带凭据、外部主机、伪造 localhost 后缀和无效端口不会成为候选。

后台从 Ghostty 已解码的最近 128 个物理行提取文本，最多 64 KiB，拼接软换行并去除终端颜色控制。只在输出 revision 改变时扫描，繁忙输出最多每秒扫描一次，全应用最多四个工作线程。读取不滚动、附着或调整终端尺寸，静止输出不会持续唤醒空闲窗口。关闭设置 → 终端 → 发现开发地址，可停止扫描并隐藏入口。

候选表示终端曾展示这个地址，不代表服务仍在监听。Retty 不主动探测端口，也不自动打开网页。远端终端点击后沿用源主机的浏览器和端口转发；同名本机地址不会被复用。点击后的布局变化会按普通分屏调整可见终端区域。

## 隔离验证

```sh
MYGO_TEST_IMAGES="$PWD/.mygo/kero-adoption" GOWORK=off go test . \
  ./internal/diagnostics ./internal/devserver ./internal/remote ./internal/rex ./internal/terminal \
  -run 'TestDevelopment|TestDiagnostics|TestDiagnose|TestHostDiagnostics|TestRecentOutput|TestAdvertised|TestDoesNotExtract|TestCandidate|TestLimitsInvalid|TestAgentDiagnostics' -count=1
```

原生浏览器验收也覆盖新功能：隔离服务启动真实 HTTP/WebSocket 页面并在 PTY 输出 URL，GUI 识别后通过与「打开」按钮相同的处理函数创建原生浏览器；独立配置中的缺失 Hook 在诊断页被检出。随后验证浏览、输入、截图、远端页和 GUI 重开，核对服务 PID、会话 ID、shell PID、终端尺寸不变。运行方式见 [浏览器窗格](browser-panes.md)。测试诊断/开发地址探针只在带私有标记及认证 token 的既有 fixture 端点中启用。

### 2026-10-23 验收记录

- 完整 Go 测试通过：`.mygo/diagnostics-devserver-complete-tests.log`，主包 69.768s。
- 无图形 CLI 测试通过：`.mygo/diagnostics-devserver-final-cli.log`，覆盖 CLI、诊断、服务、网关和屏幕包。
- 跨层 race 通过：`.mygo/diagnostics-devserver-complete-race.log`；普通与生产标签 vet 通过：`.mygo/diagnostics-devserver-complete-{vet,production-vet}.log`。
- 修正并复核后台发现对空闲 UI 的影响、设置分组位置和 GUI 控制队列截止时间。截止时间用例连续 25 次通过：`.mygo/diagnostics-devserver-deadline-recheck.log`。执行路径符号链接规范化后的定向复核：`.mygo/diagnostics-devserver-path-recheck.log`。
- MyGo 生产 `.app` 打包与 ad hoc 签名通过：`.mygo/diagnostics-devserver-package.log`；最终原生验收通过：`.mygo/diagnostics-devserver-native-verified.log`（11.768s），包含真实 Tailcat 加密流上的独立远端 Hook 配置读取。
- GUI 正常退出重开前后，daemon PID=97328、session=f7e23a2452ec、shell PID=97335、终端尺寸=71×35，均不变。测试仅拥有并清理其独立数据、进程和本地中继。
- 已查看界面截图：`.mygo/kero-adoption/hook-version-diagnostics.png`、`development-addresses.png`；系统合成画面 `development-browser-native-window.png` 确认终端地址入口和实际原生网页共同显示，并通过 Computer Use 点击「打开」确认复用现有窗格。
- MyGo 固定 fork/main 检查通过：`.mygo/diagnostics-devserver-mygo-check.log`。未修改真实 Hook 配置、覆盖安装应用、发布或重启真实后台服务；原有未提交改动保留。
