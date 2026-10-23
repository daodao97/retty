# 浏览器窗格

桌面的「视图 → 打开浏览器窗格」、命令面板或 `⇧⌘B` 会在当前终端旁打开浏览器。地址栏接受 `http://`、`https://`、`about:blank`，以及 `localhost:5173` 这样的开发地址。浏览器使用 MyGo 原生 WebView；有后退、前进、刷新和关闭按钮，也可按现有分屏快捷键继续分屏。

终端输出本地开发 URL 后，也可从窗格的「开发地址」入口直接打开并继承源主机，见 [开发地址发现](diagnostics-and-devservers.md#开发地址发现)。

浏览器与终端共用 Tab/分屏树，保存 URL、稳定浏览器 ID、分屏比例、方向、焦点和放大状态。切换 Tab 隐藏页面并保留实例；关闭浏览器销毁其 WebView、取消请求并关闭自有端口转发。正常退出 GUI 保存布局，重新打开时恢复页面。浏览器没有 PTY，不会被会话轮询当成丢失的终端补建，也不会在关闭时结束旁边的 shell。

## CLI 与 Agent

桌面可执行文件和无 UI CLI 共用入口。桌面没有 PATH 命令时，Retty 终端中可用 `"$RETTY_HOOK"` 替代 `retty`。

```sh
retty browser list --json
retty browser open http://localhost:5173 --beside SESSION_ID --json
retty browser read WEB_ID --json
retty browser type WEB_ID --selector '#name' --text 'Example'
retty browser click WEB_ID --selector '#save'
retty browser wait WEB_ID --selector '#result' --text 'Saved' --timeout 10s
retty browser screenshot WEB_ID --output /tmp/page.png
retty browser navigate WEB_ID http://localhost:5173/next
retty browser close WEB_ID
```

`open` 返回 `web-…` ID；不指定 `--beside` 时创建新 Tab。所有后续操作要求完整 ID 或至少四字符的唯一前缀，不猜测焦点、标题或序号。`status` 读取 URL、标题、加载状态和错误；`read --json` 还返回最多 32 Ki 字符的正文及最多 100 个可见交互元素的 CSS selector，密码值省略。点击、输入和等待会选择目标浏览器并初始化恢复的隐藏页面。

先读取再控制。`click/type` 的 selector 必须唯一、可见且可用；输入替换字段值并发出 input/change，不隐式提交。文本和 selector 通过 JSON 编码传给页面，连接失败不自动重放。页面已变化时拒绝操作，需重新读取；SPA/fragment URL 在读取后同步保存。`wait` 默认十秒，最大两分钟；CLI 超时返回 124，取消返回 130。截图为实际 WebView PNG（不含地址栏），最多 8 MiB 和 16 Mi 像素，以 0600 权限原子保存到 `--output`。

这些命令连接已运行的桌面 GUI 控制端点，不启动或更新会话服务。Retty 终端通过 `RETTY_DIR` 或普通 `RETTY_AGENT_SOCKET` 确定所属目录；外部 CLI 默认目录为 `RettyServer`，操作桌面通常需要显式 `--data-dir '/Users/NAME/Library/Application Support/Retty'`。哈希形式的 agent socket 无法反推出所属目录，须传 `--data-dir`。`browser help` 和 `retty skill` 可离线读取。

## 远端开发页面

浏览器从旁边的终端继承主机，也可用 `browser list --json` 返回的 host ID，通过 `browser open URL --host HOST_ID` 选择已配对电脑。远端窗格中的 `localhost`、127.0.0.0/8 和 `::1` 地址，在该主机的回环接口上打开 TCP 端口，经独立 Tailcat 加密流连接到本机临时回环监听器；HTTP、同源资源及 WebSocket/HMR 使用同一转发。URL 与布局保存远端逻辑地址，不保存临时本机端口。

该通道由网关桥接提供，不经过会话 daemon，不附着或调整终端。每个桥接最多 32 条开发连接，每个窗格转发最多 16 条连接。切换连接或关闭窗格会关闭旧监听和流；远端断开、端口未监听或旧网关不支持时显示错误，不回退为本机同名端口。网关需包含新端口扩展，GUI 更新本身不会升级运行中的旧网关。

开发服务器应接受转发后的回环 Host，并使用相对资源和同源 WebSocket URL。硬编码为另一个 localhost 端口的子资源不会自动改写；需要时分别打开该端口，或调整开发服务器的公开资源/HMR 地址。HTTPS 转发保留原始 TLS，证书仍由 WebView 校验。

## 隔离验证

```sh
GOWORK=off go test . ./internal/browsercontrol ./internal/controlcli ./internal/remote \
  -run 'TestBrowser|TestURLBoundary|TestRequestLimits|TestPortForward' -count=1
GOWORK=off go build -tags mygo_noinspector \
  -ldflags '-X github.com/egoist/mygo.production=1' -o .mygo/browser-e2e/retty .
RETTY_BROWSER_E2E=1 RETTY_BROWSER_BINARY="$PWD/.mygo/browser-e2e/retty" \
  MYGO_TEST_IMAGES="$PWD/.mygo/kero-adoption" GOWORK=off \
  go test . -run '^TestBrowserNativeAutomationAndRestore$' -count=1 -timeout 3m
```

原生验收拥有独立 GUI、服务、推送进程、数据目录、HTTP/WebSocket 页面和本地 DERP 中继；验证实际 CLI 操作、页面截图、GUI 正常退出重开、远端页面及稳定 ID/URL 恢复，核对终端进程和服务 PID 不变。全部清理仅针对测试拥有的资源。MyGo 根绘制层截图不包含嵌入的原生 WebView；页面截图使用 WebView CapturePage，整窗视觉检查使用系统合成画面。
