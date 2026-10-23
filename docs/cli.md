# Retty CLI

Retty 的含义是 **Reconnect TTY / Relay TTY**：重新连接持续运行的会话，在电脑、手机和服务器之间接续同一个终端。

CLI 让没有图形界面的 Linux/macOS 服务器也能提供 Retty 会话。桌面端和手机端使用已有的加密连接、会话列表与尺寸接管机制，服务器不需要安装桌面环境，也不需要开放公网终端端口。

脚本和 Agent 的 `list/status/read/new/split/send/wait/report`、稳定 Action ID 与内嵌使用 skill 见 [会话与 GUI 自动化](session-control.md)。这些命令连接现有服务，不启动网关或替换运行中的 daemon。

`retty browser` 通过桌面 GUI 控制浏览器窗格，包含远端开发端口、DOM 操作和截图，见 [浏览器窗格](browser-panes.md)。

## 安装与连接

下载对应平台的 CLI 压缩包；支持 Linux/macOS 的 amd64、arm64。解压后保留 `retty` 与同目录的 `libghostty-vt.so`（Linux）或 `libghostty-vt.dylib`（macOS）：

```sh
tar -xzf retty-linux-amd64.tar.gz
cd retty-linux-amd64
./retty serve
```

`retty serve` 启动后台常驻网关，输出一行 `retty://connect?...` 后返回；直接运行 `retty` 或 `retty start` 也可启动。关闭终端或退出 SSH 不会关闭网关，重复启动会复用同一个进程。桌面应用点击右上角连接按钮，在连接侧边栏点击“粘贴连接码”，即可打开或新建服务器会话。手机可以打开同一连接码。连接码是完整访问凭据，只交给自己的设备。

终端会话运行在独立的后台服务中。退出桌面或手机会保留会话及终端程序。`retty stop` 只停止连接网关，保留终端会话；再次运行 `serve` 会接回原服务和原连接身份。重启服务器、结束会话服务或显式结束会话会终止这些程序。

```sh
./retty link       # 显示保存的连接码和二维码，不启动服务
./retty status     # 输出网关/会话服务信息和会话列表，不启动服务
./retty stop       # 停止网关，保留后台终端会话
./retty version
./retty update --check  # 只检查官方正式版本
./retty update          # 校验并更新 CLI，运行中的网关自动恢复
./retty help
```

交互终端中 `serve` 和 `link` 同时显示连接码与黑白二维码，手机扫描即可连接。二维码保留完整静区，终端窗口过小时会提示所需列数和行数，放大后运行 `retty link` 重新显示。管道/重定向默认只输出连接码；可加 `--qr` 强制输出二维码。

状态默认保存在用户配置目录下的 `RettyServer`，Linux 通常为 `~/.config/RettyServer`。使用 `--data-dir /absolute/path` 或 `RETTY_DIR` 指定其他目录。`serve` 和 `link` 支持 `--link-file /private/path/connect.txt`；默认 `<data-dir>/connect.txt`，权限 0600。后台日志在 `<data-dir>/gateway.log`，日志不记录连接码。重连沿用 `<data-dir>/remote/identity.json`，不要删除身份文件或使用多个服务同时共享它。需要在前台查看运行情况时使用 `retty serve --foreground`，此时 Ctrl+C 停止网关并保留会话。

Linux 需要可用的 `/dev/ptmx`、`/dev/pts`、用户 shell、glibc 和系统 CA 证书（如 `ca-certificates`）；Alpine/musl 不在当前构建范围内。服务器需要访问 Tailcat 所用的公网中继。Agent CLI 在服务器安装，正常终端命令、Tab 补全和窗口尺寸变化使用现有 PTY 协议。无桌面剪贴板的服务器暂不支持图片粘贴。

## 常驻运行（Linux systemd 用户服务）

将 `retty` 和 `libghostty-vt.so` 放入 `~/.local/lib/retty/`，创建 `~/.config/systemd/user/retty.service`：

```ini
[Unit]
Description=Retty encrypted terminal gateway
After=network-online.target

[Service]
ExecStart=%h/.local/lib/retty/retty serve --foreground
Restart=on-failure
RestartSec=3
KillMode=process
UMask=0077
StandardOutput=null

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now retty
~/.local/lib/retty/retty link
```

`KillMode=process` 让停止或重启连接网关时保留后台会话进程；`StandardOutput=null` 避免连接凭据进入 journal。需要退出 SSH 后继续运行或开机启动时，由管理员为该用户开启 systemd linger。Agent 所需 PATH、代理、语言环境应在用户服务环境中配置，shell 也会加载自己的登录配置。

默认后台运行支持退出 SSH 后继续连接；开机自动启动、异常退出后自动重启由上述 systemd 用户服务管理。该模式使用 `systemctl --user stop/restart retty` 管理网关，不能同时再启动另一个前台实例。

## 自身更新和从桌面更新远端

`retty update --check` 检查官方 GitHub Release 的最新正式版本，`retty update` 下载对应系统/架构的 CLI 压缩包及 SHA-256 文件。仅接受固定仓库 `daodao97/retty` 的正式版本，不接受自定义 URL、安装路径、命令或降级；开发版和预发布版本需手动更新。当前安装目录须由运行用户可写，不自动使用 sudo。

更新先解压到安装目录下 `.retty-versions/`，检查归档路径、大小、必需文件、实际版本、终端库加载和已有会话协议兼容性，再原子切换稳定的 `retty` 入口。SHA-256 文件和安装包均从官方 HTTPS Release 获取；CLI 包目前没有独立的离线签名。旧二进制和库保留在原位置或 `.retty-versions/previous-*`，不覆盖运行中使用的文件，不自动清理旧版本。

运行中的网关接受更新后独立完成工作，不因发起端关闭终端或 Tailcat 连接断开而取消。它关闭自己的桥接连接并释放自己的锁，然后以 `exec` 切换新版，保留 PID、参数、数据目录和连接身份；systemd 的 `--foreground` 模式也走同样流程，不需要重启 systemd 服务。连接会短暂断开，桌面/手机按现有恢复流程重新连接。未运行网关时只更新安装包，不自动启动网关。

在桌面打开目标远端会话，命令面板或 Shell 菜单选择 **「更新远端 Retty…」**，检查当前/最新版本后点击具体版本的更新按钮。更新控制和进度使用独立 Tailcat 加密流，不向 PTY 输入命令，不附着或改变尺寸。远端 CLI 更新网关；远端 macOS 桌面版复用 MyGo 原生更新器，下载、验签及安装与本机更新窗口共享同一任务，完成后只正常重开 GUI。尚不支持远程更新的旧端须先手动安装一次新版。CLI 的状态保存在网关数据目录的 `update.json`，桌面版保存在 `desktop-update.json`，重新连接可继续查询。

下载、哈希或运行时验证失败不会切换安装入口。更新只升级 CLI 网关：已运行的会话服务继续使用旧代码，任务与 shell 保留。需要升级会话服务时，先完成其任务，再安排服务更新。不要批量结束 Retty/Agent 进程或删除真实状态。

## 从源码构建

```sh
GOWORK=off ./scripts/build-cli.sh --platform linux/amd64
GOWORK=off ./scripts/build-cli.sh --platform all
```

产物在 `build/cli/`，每个 tar.gz 同时带有 SHA-256 校验文件。构建下载固定 manifest 中的 Ghostty VT 动态库并校验其哈希，压缩包运行时无需下载。只构建二进制可使用 `GOWORK=off CGO_ENABLED=0 go build -tags retty_cli -o retty ./cmd/retty`；运行仍需要对应 VT 动态库。

从 [GitHub Releases](https://github.com/daodao97/retty/releases/latest) 下载对应系统和架构的 tar.gz 及 `.sha256`。**Release** 工作流在 `v*` 标签推送或手动运行时构建四个平台的 CLI 与 Universal DMG，全部验证通过后发布。下载后可用 `sha256sum -c retty-linux-amd64.tar.gz.sha256` 校验；macOS 使用 `shasum -a 256 -c`。

**Build CLI** 工作流仍在 `main` 推送和手动运行时提供日常构建，对应的 `Retty-cli-*` Actions Artifacts 保留 14 天。
