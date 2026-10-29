# UI 设计规范

Retty 的界面是 MyGo 的即时模式 UI。这份文档约定视图里使用的颜色、间距、圆角、字号和组件，让桌面端、手机端，以及 board 这类新视图看起来属于同一个应用。token 定义在 `style.go`，这里说明什么场景用哪一个。`docs/settings-design/` 里的截图是视觉参考。

## 原则

- **终端是主角**：chrome 要安静，靠中性灰和少量透明度区分层级。强调色只用于主操作和选中态，不用来装饰。
- **light 和 dark 成对设计**：每个颜色 token 都在 `lightColors` 和 `darkColors` 里各有一份取值。dark 主题延续 iTerm2 风格的中性炭灰，不带色相。
- **语义优先**：视图只引用 token，不直接写数值。新需求先看现有 token 能否表达；确实不够时，先在 `style.go` 增加 token，再在视图里使用。

## 颜色

通过 `k := colorsOf(c)` 取得。

| 用途 | token |
| --- | --- |
| 正文 / 次要 / 弱化文字 | `text` / `textMuted` / `textFaint` |
| 图标默认色（hover 时变为 `text`） | `iconMuted` |
| 交互底色 | `hover`、`pressed` |
| 浮层面板、边框、选中行、遮罩 | `panel`、`panelBorder`、`panelSel`、`backdrop` |
| sidebar 的底色和右边线 | `sidebar`、`sidebarBorder` |
| 需要用户作答的对话框遮罩（比 `backdrop` 深） | `scrim` |
| 用量条等进度条的填充色（比 `accent` 柔和） | `meter` |
| 小弹层（如连接指标）的阴影 | `popoverShadow` |
| 对话框（如 palette）的两层阴影 | `dialogShadow`、`dialogShadowNear` |
| 手机底部 sheet 的遮罩 | `sheetScrim` |
| 文件面板的底色 | `canvas` |
| 聚焦卡片的边框和阴影 | `cardBorderFocused`、`shadowFocused` |
| 主操作、选中 | `accent` |
| 实心主按钮、危险按钮上的文字和图标 | `onAccent` |
| 状态：进行中 / 成功 / 警告 / 失败 | `info` / `success` / `warning` / `danger` |
| agent 忙碌 / 需要关注（侧栏圆点） | `busy` / `attention` |

- 状态色的浅色背景用 `tone.Alpha(0.14)`，参见 `statusPill`，不要另外定义一个颜色。
- 分隔线（tab 之间、栏与栏之间、标题栏下方、分屏）用 `headerBorder`，light 和 dark 都要画出来，light 下是 8% 黑的细线。
- 状态色（`info`/`success`/`warning`/`danger`）会直接用作 pill 文字，light 和 dark 各取一组：light 用更深的一档，dark 用更亮的一档。`TestStateColorsReadAsText` 要求它们和 `textFaint` 在两种主题的终端底色和 sidebar 底色上都不低于 4.5:1，改色值时先跑这个测试。
- `textMuted` 和 `textFaint` 在两种主题下都要拉开层级；`textFaint` 在最深的底色（light 的 sidebar）上也要保持 4.5:1。
- 文字和背景的对比度至少 4.5:1，可以用 `appearance_test.go` 里的 `colorContrast` 检查。
- 终端自己的配色是 `lightTerm`/`darkTerm`，只用于终端，不要用在 chrome 上。
- 整页覆盖 MyGo `ui.Theme` 的页面统一用 `style.go` 里的 `pageTheme`：设置页、连接抽屉和用量面板共用，全部从 `colors` 取值（底色 `canvas`/`track`、强调色 `accent`、控件圆角 `radiusM`）。手机端用 `connectionTheme`。不要再为单个页面另写一套颜色。
- 程序图标的颜色是图标本身的一部分，写在 `programs.go` 的程序表里（另有 `paneProgram` 里浏览器、Diff、文件三项），不属于界面配色。

## 间距

采用 4pt 网格：`space2XS 2`、`spaceXS 4`、`spaceS 6`、`spaceM 8`、`spaceL 12`、`spaceXL 16`、`space2XL 24`、`space3XL 32`。

- 图标和文字之间、pill 内部用 `spaceXS` 或 `spaceS`。`spaceS` 只用于紧凑的行内场景。
- 列表行之间、按钮组内部用 `spaceM`。
- 卡片内边距、分组之间用 `spaceL` 或 `spaceXL`。
- 卡片标题与操作之间、页面的上下边距用 `space2XL`。
- 页面左右边距、页面里各大区块之间用 `space3XL`。
- 需要和别处对齐时用组合值，并写注释说明对齐的是什么。例如 board 列表头的左边距是 `spaceM+spaceL`，正好对齐列表行里的文字。
- 不使用 3、5、7、9 这类奇数值。光学对齐需要 1px 微调时，在调用处写注释说明原因。

## 圆角

`radiusS 4`：pill、输入框、小标签，以及 sidebar 里 30px 左右的紧凑行。`radiusM 8`：按钮、列表行、菜单项。`radiusL 12`：卡片、面板、对话框。`radiusXL 16`：手机端卡片。`radiusSheet 20`：手机底部 sheet。

圆点和胶囊的圆角取高度的一半，例如 `statusDot` 是 7px 高，圆角 3.5。

## 字号

| token | 大小 | 用途 |
| --- | --- | --- |
| `textMicro` | 10 | 只用于 sidebar 的密集元信息（分支名、计数、时间、提示文字）和大写角标（如用量面板的套餐名） |
| `textCaption` | 11 | 角标、时间、计数、辅助说明，sidebar 的分组标题（加 600 字重） |
| `textSmall` | 12 | 侧栏、列表行、按钮 |
| `textBody` | 13 | 正文、表单、设置项 |
| `textTitle` | 15 | 卡片和面板标题、手机端列表 |
| `textHeading` | 18 | 页面标题 |
| `textDisplay` | 22 | 一眼要读到的数字，例如指标卡片里的值 |

手机端按 iOS 的字号体系另有一组，比桌面大，小于 14 的字号与桌面共用 `textCaption`/`textSmall`/`textBody`：

| token | 大小 | 用途 |
| --- | --- | --- |
| `mobileSubhead` | 14 | 次要说明、卡片标题 |
| `mobileBody` | 15 | 列表行、正文 |
| `mobileCallout` | 16 | 输入框、按钮，也是手机主题的默认字号 |
| `mobileHeadline` | 17 | 页面和 sheet 的导航标题 |
| `mobileTitle` | 18 | sheet 和确认框的大标题 |
| `mobileLargeTitle` | 32 | 启动页标题 |

- 字重：正文 400，强调 500，标题 600。不要用加大字号来表达强调。
- 终端字体是 `termFont`（JetBrains Mono 11.6），由用户的字号设置缩放，不属于上面的阶梯。
- 不新增半号字号，例如 10.5、11.5、12.5。

## 组件

已有组件优先复用，不要在单个视图里另写一份相似的：

- 图标按钮：`iconButton`（`view.go`）
- 状态：`statusDot`、`statusPill`（`board.go`），状态到颜色的映射是 `stateColor`/`runVerdict`
- 卡片、行、分组：目前有 `boardCard`/`boardRow`/`boardSection`、`desktopConnectionRow`/`desktopConnectionSection`、`mobileCard`/`mobileListRow` 三套，计划合并成一套共享组件。在合并之前，新视图优先使用 board 这一套。

交互状态的规则：

- hover 用 `hover` 底色，按下用 `pressed` 底色。
- 键盘聚焦（`FocusVisible`）和 hover 的表现相同。
- 颜色过渡统一为 `ui.ElementTransition{Colors: true, Duration: 120 * time.Millisecond}`。
- 可点击元素要设置 `Role` 和 `Label`，供读屏和 tooltip 使用。

## 平台差异

手机端的触控目标至少 44pt，卡片使用 `radiusXL`，列表文字使用 `textTitle`。除此之外沿用同一套 token，不要为手机端另外定义一套颜色。

## 迁移状态

token 已经加入 `style.go`。

- **已完成**：主包里所有视图，包括桌面端（board、sidebar 和 tab、设置页、连接页和用量面板、文件面板、浏览器、palette、diagnostics）和手机端（`mobile*.go`）。页面级的主题覆盖只剩 `pageTheme`（桌面）和 `connectionTheme`（手机），都在 `style.go`。
- **守护测试**：`style_tokens_test.go` 扫描主包的视图源码，发现直接写的颜色（`ui.Hex`/`ui.RGBA`/`ui.RGB`）或数字字号（`FontSize(13)` 这类）就失败。`style.go`、`programs.go`（程序图标色）、`pairing.go`（二维码）整个跳过，另有程序图标色和二维码白底两类行放行。新增例外时在测试的允许名单里写明原因。

间距和圆角暂未纳入守护测试：圆点和胶囊取半高的圆角、对齐用的缩进等合理的数值较多，靠评审和本文档约束。

视图测试会在 1000×620 的窗口里点击按钮，而 `Tester.Click` 只检查坐标在窗口内，不检查元素是否被滚动区域裁掉。行高变大可能把页面末尾的按钮挤到可见范围外，测试就会点空。迁移后如果有点击类测试超时，先检查目标按钮渲染后的位置和高度。

sidebar 的 tab 列表高度是按行高加间距估算的，写在 `sidebarLayout` 的 `listHeight` 里。修改 sidebar 的行高、间距或内边距时，要同步更新这个估算。

每迁移一个视图，都用 `MYGO_TEST_IMAGES=<dir> GOWORK=off go test -run '<视图>' .` 分别渲染迁移前后的截图做对比。归档会带来几个像素的变化，这是预期内的，但布局结构不应该变。

下面这些地方可以保留直接写的数值：

- 图形内部的几何尺寸，例如迷你图的柱宽和圆角。
- 圆点和胶囊取高度一半的圆角。
- 已经写了注释说明原因的基线微调和对齐缩进，例如连接页分隔线跳过图标的 48px 左缩进。
- 二维码的白底：扫码需要白底，与主题无关。
