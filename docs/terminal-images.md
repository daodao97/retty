# 终端图片

## 向远端 Codex 粘贴图片

桌面连接远端 Retty 会话，在 Codex 内按 `⌘V`、`Ctrl+V` 或使用粘贴菜单时，Retty 先读取本机 PNG 剪贴板，通过已有独立文件通道上传到会话所在主机，收到完整确认后再以 bracketed paste 插入远端图片路径。手机的 Codex 图片粘贴使用同一通道。不会向远端发送 Ctrl+V，不读取远端 X11/Wayland 剪贴板，也不提交回车；用户仍可补充说明后自行发送。

上传遵守 PNG 32 MB / 3200 万像素限制并检查完整解码。本机临时文件权限 0600，操作结束后只清理本次临时目录；远端图片使用既有私有上传批次，保留给 Agent 读取。取消、切换窗格、切换 Agent 对话、重连或手机进入后台后不插入迟到路径。普通 Ctrl+V 在没有图片处理时继续传给终端；其他程序原有粘贴行为保留。

远端需支持已有的 `upload-files` 扩展；不支持时显示网关升级提示，不尝试粘贴本机路径或回退到远端剪贴板。此修复只更新客户端，不要求重启远端 Codex、共享 daemon 或 PTY。普通 ssh/mosh 嵌套连接仍不具备 Retty 文件通道，不能把本机路径当成远端路径使用。

```sh
GOWORK=off go test . ./internal/remote ./internal/terminal \
  -run 'TestRemoteCodexImage|TestMobileCodexImage|TestHeadlessCodexImage|TestCtrlVImage|TestRemoteFileDrop' -race -count=1
```

## 图片路径悬停

鼠标停留在终端打印的图片路径上约 350ms，Retty 在终端旁显示预览，不需要按 Command。原有 Command 点击、文本选择和终端鼠标输入保持原来的行为。支持 PNG、JPEG、GIF 首帧、WebP 和 BMP；HTTP 图片 URL 不会自动下载，SVG 不作为图片执行。

相对路径以该窗格的当前工作目录解析，`~/` 使用该主机的主目录。Retty 远端会话通过对应连接的独立文件通道读取；普通 ssh/mosh 没有这样的文件连接时跳过预览，不读取本机同名路径。读取捕获主机、连接和规范路径，重连后不会把旧请求转到新连接。

预览先检查普通文件及大小，再用文件版本读取完整内容；文件限制 4 MiB，单边最多 8192 像素，总像素最多 16 Mi。解码前检查图片头，全局最多两个解码工作线程。移到其他路径、离开路径、隐藏窗格、打开完整文件工作台或关闭终端时取消请求；隐藏后释放缓存图片，迟到结果不能替换新预览。预览失败保持安静，点击链接仍可通过正常文件入口查看具体错误。

## Kitty graphics

Retty 的终端副本已接入 MyGo 的 Kitty 图片绑定与三层绘制：背景之下、文字之下、文字之上。支持直接传输的 RGB/RGBA 与 PNG、分块、zlib、指定单元格尺寸、裁剪、像素偏移、层级、替换和删除；每个终端默认最多保存 8 MiB 图片数据，PNG 解码也在分配像素前检查单张尺寸。虚拟 Unicode placeholder 放置目前不绘制。文件、临时文件和共享内存传输关闭，远端字节不会使查看端读取或删除本机文件。

桌面与无图形 CLI 的会话服务共用图片解码。后台屏幕在首次查看前已有单元格像素几何；新查看端可获取已经存在的图片，进程退出后也保留可见图片产物。普通历史读取仍返回较小的文本快照。

图片快照发送直接像素数据和可见位置，不携带源主机的文件路径；像素传输按图片 generation 缓存，并与文字画面一起恢复。窗口滚动时，部分移出顶部的图片裁剪到可见区域。恢复后保留最终光标和 origin 模式，不重放铃声、剪贴板或通知。

服务通过 `screen_graphics` 公布能力，新的查看端在附着时请求它。未请求的旧查看端保持原有 4 MiB 文本状态帧限制；协商图片后帧上限为 16 MiB，图片效果之外的事件队列仍使用原有界限。独立查看连接不会改变源 PTY 尺寸。

本次同步 MyGo 的图片部分时，保留 Retty 的 iOS 静态符号生成、搜索、重排、CLI 和屏幕同步实现；没有替换整份 terminal。iOS 静态表由 `scripts/generate-ios-vt.py` 生成，新增符号均在当前固定的 Ghostty 对象中。

## 更新与验证

悬停预览通过 GUI/网关的现有文件服务工作，不需要重启会话服务。Kitty 解析和图片快照属于会话服务能力；有真实任务运行时保留旧服务，任务结束后再更新，不为启用图片结束现有会话。

隔离验证覆盖解码字节及像素上限、版本读取、取消与迟到结果、远端主机路由、PNG 各像素格式、图片替换/删除、层级/裁剪/光标、后台屏幕恢复、真实服务的普通与同步连接、重连及进程退出后的图片。

```sh
GOWORK=off go test . ./internal/rex ./internal/terminal/... ./internal/imagepreview ./internal/remote \
  -run '^(TestKitty|TestPNGLimits|TestImageHover|TestDesktopImageHover|TestPreview|TestImagePreview)' -count=1
```

渲染验收截图位于 `.mygo/kero-adoption/terminal-image-hover.png`、`kitty-terminal.png` 和 `kitty-service-reconnect.png`。
