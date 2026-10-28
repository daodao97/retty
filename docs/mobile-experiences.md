# 手机开发预览、订阅用量和锁屏状态

## 开发页面预览

连接主机后，在会话列表设置或终端右上角「会话工具」打开「开发页面预览」，手动输入主机上的 `http://localhost:3000` 等地址。支持 localhost、IPv4/IPv6 回环地址和显式端口，保留页面路径、查询参数及锚点，不扫描开发端口。

Retty 在手机上建立临时回环监听，通过现有加密连接转发到主机开发服务，由系统应用内浏览器显示。HTTP、WebSocket/HMR 都使用同一端口通道。预览关闭、连接断开或应用进入后台时清理自己的转发，不关闭开发服务器、不发送终端输入。服务需要使用相对链接或与当前页面同源的 WebSocket；页面硬编码的主机端口不会自动改写。HTTPS 仍需通过系统证书校验。

## 订阅用量汇总

主机首页右上角、会话列表设置和会话工具均提供「订阅用量汇总」。按已保存的主机分组，读取各主机当前 CLI 登录账号的 Codex / Claude 额度、重置时间和 Codex 重置卡数量。各订阅窗口分别显示，不相加百分比，也不把离线、未登录或读取失败当成零用量。

用量通过独立的认证扩展连接读取，不切换当前主机或终端。最多同时查询两台主机；主机侧合并并发请求、缓存三分钟，手动刷新最短间隔十秒。读到的旧数据保留更新时间，失败时显示提示。进入后台或关闭面板会取消手机侧查询。

CLI 自己处理凭据，凭据、配置路径、账户内部标识和重置卡兑换 ID 均不传到手机。手机只读，不提供用卡操作。网关需要更新后的 Retty GUI 或 CLI；不必重启会话 daemon。

## 锁屏实时状态

需要 iOS 16.2+，并在系统设置允许 Retty 实时活动。在终端「会话工具」选择「在锁屏显示此会话」；一次跟踪一个会话，开关可停止活动。锁屏和灵动岛显示运行、等待、完成或失败，点按卡片返回已配对主机的对应会话。

锁屏内容只包含主机显示名称、状态及可用进度，不包含命令、路径、提示词和 Agent 回复。深链接只携带稳定主机/会话标识，不携带配对凭据。完成/失败后结束活动，卡片最多保留十分钟；状态超过两分钟未更新时显示连接中断提示。

前台使用 MyGo ActivityKit 接口；后台由当前网关读取既有会话状态并通过 APNs 的 `liveactivity` 通道更新。它不依赖手机后台轮询，也不重启既有 daemon 或通知 worker。主机网关必须持续在线，且配置与 `com.daodao.retty` 匹配的 APNs provider；设备开发签名使用 sandbox。ActivityKit 令牌与普通通知令牌不同，仅保存在网关内存，轮换时替换，结束/失效后删除，最长八小时过期。网关退出后卡片会变为过期状态，回到手机重试即可重新注册。

令牌注册、版本校验、过期、并发发送、内容去重与失效令牌清理由 MyGo 的无界面 `apns.ActivityManager` 统一处理。Retty 负责配对连接的授权校验、把设备与活动绑定到会话、读取会话状态，以及生成锁屏文案和深链接。管理器不创建默认轮询；网关只在存在有效活动时读取既有状态，保持每分钟刷新、两分钟过期提示、结束后十分钟移除的策略。状态读取期间令牌或会话绑定改变时，旧快照不能向新的绑定提交更新。

## 构建与隔离验收

MyGo 的通用浏览器、ActivityKit 桥接和 APNs 通道在 fork 的 main 上维护。Retty 的 `resources/ios/RettyLiveActivity.swift` 只负责 WidgetKit 界面。`mygo.json` 显式选择 native support，并嵌入 `com.daodao.retty.RettyActivity` 扩展。

开发签名继续使用手机已有的主 App profile。扩展另外使用同 Team、覆盖该设备与扩展标识的 profile，传入 `IOS_ACTIVITY_PROVISIONING_PROFILE`；不需要给扩展增加普通通知权限。正式商店发布则需要匹配扩展的分发 profile，在签名配置中补齐 `ios.signing.exportProfiles`。

真机验收只创建独立 Python/PTY、HTTP 服务、加密网关和用量替身，APNs provider 只读取现有配置。以下命令会覆盖安装当前开发包，并在测试专用 Keychain 命名空间运行：

```sh
GOWORK=off IOS_TEST_FLOW=features \
 RETTY_IOS_FEATURES_PROVIDER_DIR="$HOME/Library/Application Support/Retty" \
 IOS_TEAM=YOUR_EXISTING_TEAM IOS_DEVICE=YOUR_CONNECTED_UDID \
 ./scripts/test-ios.sh
```

测试验收原生预览页面、只读用量、ActivityKit 创建，以及 App 退到后台后的 running/waiting/completed APNs 接受回执；同时核对测试会话身份和无终端输入。结果保存在 `.mygo/ios-test`。测试结束后使用不带 `RETTY_UI_TEST` 的正常环境启动 Retty，不能卸载正常 App。
