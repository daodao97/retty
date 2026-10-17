package org.mygo

import android.app.Activity
import android.content.Context
import android.os.Build
import android.view.KeyEvent
import android.view.MotionEvent
import android.view.Surface
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.view.WindowInsets
import android.text.InputType
import android.view.inputmethod.EditorInfo
import android.view.inputmethod.InputConnection

/**
 * 承载 Go 侧渲染的 SurfaceView。
 *
 * ============================================================================
 * 🔴 面创建/销毁的顺序陷阱（本文件最重要的一段注释）
 * ============================================================================
 * AOSP 官方说明：**Activity 的生命周期与 Surface 的生命周期是两个独立的状态机**
 * （参见 SurfaceHolder.Callback 文档："the surface is created and destroyed at
 * different times than the activity"）。因此：
 *
 *   · 切后台再回前台，事件序列是 surfaceDestroyed → (Activity onPause/onStop/onStart/onResume)
 *     → surfaceCreated → surfaceChanged。**不是** onPause/onResume 一对一映射。
 *   · 同一份 surface 可能在一个 Activity 实例内被创建/销毁多次；
 *     也可能 surface 还活着而 Activity 已经在 onStop。
 *
 * 由此推出本工程的两条硬约束：
 *   1) surfaceDestroyed() 里 **不做任何销毁 Go 侧状态的动作**（不 detach、不 stop 渲染线程、
 *      不释放 context）。Go 侧负责保活，Java 侧只做「事件转发」。Java 侧一旦在这里
 *      越权销毁，回前台时 Go 侧拿到的是一个已死的 context，表现为黑屏或崩溃。
 *   2) surfaceCreated() 之后 surface **未必**立刻可用尺寸；尺寸一律以
 *      surfaceChanged()/onSizeChanged() 为准，不在 surfaceCreated 里假设 w/h。
 *
 * ============================================================================
 * 事件转发
 * ============================================================================
 * · 触摸：坐标用 MotionEvent.getX()/getY()。这两个 API 返回的**本来就是相对本 View
 *   左上角的坐标**（view 内坐标），不能用 getRawX()/getRawY()（那是屏幕坐标）。
 *   这里显式用 getX(actionIndex)，在注释里点明这一点，避免后人误改成 raw 坐标。
 * · 按键：需要本 View 拿到焦点才会收到 onKeyDown/Up，所以构造时设
 *   focusable / focusableInTouchMode，并由 Activity 调 requestFocus()。
 *
 * ============================================================================
 * 多指：走 MygoInput，不再走 Native.onTouch
 * ============================================================================
 * Native.onTouch(int action, float x, float y, long eventTimeMs) 是**单点**契约：
 * 它只能表达一个指针，多指的 pointerCount / 各指坐标 / pointer id 传不下去。
 * 所以触摸与按键改由 MygoInput 整事件转发（一次调用带整组指针 —— 见 MygoInput.kt），
 * Go 侧再映射成 platform.SurfaceEvent：多指的完整信息在那一层作为契约之外的
 * payload 产出，方案见产出文档 T32.md。
 *
 * 🔴 为什么是「替换」而不是「同时转发」：两条通道同时喂同一批事件，等 PM 收尾把
 *    两边都接到内容上时，同一次按下会被投递两次。一件事一个入口。
 *    Native.onTouch / Native.onKey 本身没动（那是单点契约，仍可用）；若要退回旧路，
 *    把下面两处 MygoInput 调用换回 Native.onTouch / Native.onKey 即可。
 *    面生命周期（surfaceCreated/Changed/Destroyed、onSizeChanged）仍走 Native。
 * · 滚动：鼠标/触控板的 ACTION_SCROLL 在 onGenericMotionEvent 里，与触摸同一条
 *   MygoInput 通道（都是 MotionEvent，字段口径一致）。
 */
class MygoView(context: Context) : SurfaceView(context), SurfaceHolder.Callback {

    init {
        // 自己当回调接收者；不要在 Activity 里另外注册，避免一份 surface 两个回调节点。
        holder.addCallback(this)
        isFocusable = true
        isFocusableInTouchMode = true
        // 把承载文字输入的 View 交给 Go 侧（Java → Go，一次性登记）：Go 才能在自己那侧的
        // 可编辑元素拿到键盘时唤起软键盘。必须在第一次要 InputConnection 之前登记，
        // 所以放 init 而不是 onCreateInputConnection（那时系统还没被要求弹键盘）。
        MygoIme.bindView(this)
        // requestApplyInsets 放在 onAttachedToWindow，不能放这里：
        // 系统只给**已经 attach 到窗口**的 View 派发 inset，构造函数里调
        // 是一次空操作（实测：这么写 nativeOnSafeArea 一次都没被调用，
        // Go 侧的 safe area 恒为零）。见 onAttachedToWindow。
        // 🔴 surface 默认在所有窗口 content 之下，键盘（另一个窗口）弹出来时
        // 会被这块 surface 盖住：dumpsys 里 InputMethod 窗口 isVisible=true、
        // mInputShown=true、输入法进程前台，屏幕上却只剩我们自己的工具条。
        // MEDIA_OVERLAY 把这块 surface 提到 media 层之上，键盘就在它上面了。
        //
        // 只能 MediaOverlay，不能 setZOrderOnTop(true)：那会把 surface 提到
        // **窗口**之上，键盘同样被盖，而且状态栏/通知栏也会被压住。
        setZOrderMediaOverlay(true)
        // 保持这一层 View 极简：SurfaceView 之下不再叠别的 View。
        // 🔴 不要在 setContentView(SurfaceView) 之后又往同一个容器里加会改变
        //    surface z-order 语义的 View（那会让 SurfaceView 从「独立合成层」
        //    退化成「与父 View 同步的层」，出现闪烁/遮挡）。当前实现就这么简单。
    }

    // ─────────────────── SurfaceHolder.Callback ───────────────────

    override fun surfaceCreated(holder: SurfaceHolder) {
        // 只转发。不要在这里假设尺寸，也不要在这里 init Go 渲染上下文
        // （尺寸以 surfaceChanged / onSizeChanged 为准）。
        Native.onSurfaceCreated(holder.surface)
    }

    override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {
        // dpi 用当前 display 的 densityDpi。注意：Surface 尺寸变化（分屏、折叠、旋屏）
        // 都会走到这里，Go 侧据此重建 framebuffer / 视口。
        Native.onSurfaceChanged(width, height, resources.displayMetrics.densityDpi)
        // 触摸坐标是 view 像素，Go 侧要换算成 DIP；密度的唯一来源就是这里。
        // 与 onSurfaceChanged 同一个值一起报过去，两者不会不一致。
        // refreshHz 同源：先要最高档，再把"这一档是多少"报给 Go（见下）。
        //
        // 🔴 面板会自己降频（空闲 60Hz、触摸升到 120Hz），而 SurfaceView 自绘**不走**
        // Android 的帧管线，系统没有任何理由替我们提频 —— 不提就一直是它当下的那一档。
        // 实测：dumpsys SurfaceFlinger --latency 首行静止时 16666667（60Hz），
        // 一触摸变成 8333333（120Hz），而 FramePhases 量到的帧间隔在 55ms 上下，
        // 即量的是系统给的那一档，不是面板的能力。所以两件事都要做：
        // requestMaxRefreshRate() 去要最高档，currentRefreshRate() 把结果报给 Go。
        requestMaxRefreshRate()
        MygoInput.onDisplayInfo(resources.displayMetrics.densityDpi, currentRefreshRate())
    }

    /**
     * 当前 display 模式的刷新率（Hz），拿不到时 0。
     *
     * 取 `display.mode.refreshRate` 而不是 `display.refreshRate`：前者是
     * "这个窗口现在挂在哪个 mode 上"，后者在旧 API 上是"首选模式"，
     * 在自适应刷新率的设备上两者可以不同 —— 我们要的是前者。
     */
    private fun currentRefreshRate(): Float {
        val d = display ?: return 0f
        return d.mode?.refreshRate ?: 0f
    }

    /**
     * 让系统知道这块 surface 想以面板的最高刷新率出帧。
     *
     * 两件事一起做，因为 Android 上是两个独立的开关：
     *  · WindowManager.LayoutParams.preferredDisplayModeId 选**显示模式**
     *    （面板档位），API 23 起可用 —— 这是真正切档的那一下。
     *  · Surface.setFrameRate(...) 告诉 SurfaceFlinger 这块 buffer 的
     *    期望节奏（API 30 起）。只设前者，合成器仍可能按旧档调度。
     *
     * 都做了版本守卫：minSdk 是 24，两个 API 分别在 23 / 30，低版本直接跳过 ——
     * 那时面板通常只有一个档，也就没有"提频"这回事。
     *
     * 用 FIXED_SOURCE 而不是 default：这是渲染内容自己的节奏，不是"跟着触摸走"；
     * 用 default 的话松手就掉回 60Hz，量到的数又变了。
     */
    private fun requestMaxRefreshRate() {
        val d = display ?: return
        val best = d.supportedModes.maxByOrNull { it.refreshRate } ?: return
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            // 窗口上选模式，不是 surface 上：SurfaceView 的模式由宿主
            // WindowManager 决定，直接改 surface 会被合成器忽略。
            val activity = context as? Activity ?: return
            activity.window.attributes = activity.window.attributes.apply {
                preferredDisplayModeId = best.modeId
            }
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            holder.surface?.setFrameRate(
                best.refreshRate,
                Surface.FRAME_RATE_COMPATIBILITY_FIXED_SOURCE
            )
        }
    }

    override fun surfaceDestroyed(holder: SurfaceHolder) {
        // 🔴 严禁在此销毁 Go 侧 context（见文件头注释 #1）。
        // Go 侧保活；Java 侧只转发事件。
        Native.onSurfaceDestroyed()
    }

    // ─────────────────── 尺寸变化 ───────────────────

    override fun onSizeChanged(w: Int, h: Int, oldw: Int, oldh: Int) {
        super.onSizeChanged(w, h, oldw, oldh)
        // View 尺寸（含 inset/装饰偏移后的实际可用区）与 Surface 尺寸可能不同，
        // 两者都转给 Go 侧，由 Go 侧决定以哪个为准（这里不做取舍，避免丢信息）。
        Native.onSizeChanged(w, h)
        // 尺寸变了 inset 也可能变（旋转、分屏、键盘）。这里再报一次是**必须**的：
        // onApplyWindowInsets 只在 inset 真正变化时被系统调用，而旋转时
        // 状态栏从上面跑到侧面、手势条从下面跑到侧面，值变了但系统未必重发。
        reportSafeArea()
    }

    // ─────────────────── 系统栏让位（safe area） ───────────────────

    /**
     * attach 之后才能要到 inset。
     *
     * 🔴 这个重写是必需的，不是保险：`requestApplyInsets()` 在构造函数里是
     * **空操作** —— 系统只给已 attach 的 View 派发 WindowInsets，而 View 在
     * `setContentView` 之前还没 attach。第一版把它放在 init 里，症状是
     * `nativeOnSafeArea` 一次都没被调用（logcat 里搜不到），Go 侧 safe area
     * 恒为零，页面照样压在状态栏下 —— 而"没调"和"调了但是零"在画面上
     * 长得一模一样，只能靠日志分辨。
     */
    override fun onAttachedToWindow() {
        super.onAttachedToWindow()
        requestApplyInsets()
    }

    /**
     * 把系统栏占掉的区域报给 Go 侧。这是**唯一**一个"系统画在我们上面"的信息来源：
     *
     *  · 本 App 的 theme 是 `Theme.Black.NoTitleBar.Fullscreen`，内容从屏幕
     *    左上角开始画 —— 状态栏、刘海、手势条**都压在内容上**。
     *  · Go 的布局引擎看不见任何系统栏，它只知道自己的画布有多大。
     *    所以"内容该躲开哪几边、躲多少"只能在 Java 侧问系统，再由 Go 侧用。
     *
     * 单位换算：WindowInsets 的 getInsets() 给的是**设备像素**，而 Go 的布局
     * 是 DIP（与 CSS px 同量级）。这里除以 density 后再报，Go 侧拿到的就是
     * 能直接当 padding 用的值。四舍五入而不是取整：3.5 DIP 取 3 会让内容
     * 差半个像素压在栏下，取 4 只是多 0.5 的留白。
     *
     * 为什么用 `getInsets` 家族而不是 `getSystemWindowInsets`（后者已废弃）：
     *  · systemBars() = 状态栏 + 导航栏 + 灵动岛（displayCutout 之外的那部分）
     *  · displayCutout() = 刘海/挖孔
     *  两者取并集才是"设备自己画的、会压住内容的东西"。
     *
     * 🔴 IME（键盘）单独报，不算进 SafeArea。
     *
     * 原来这里写着"IME 不算，它有独立的 adjustResize 路径"。那个前提在
     * targetSdk 36 上不成立了：Android 16 强制 edge-to-edge（窗口 attrs 里
     * 就是 EDGE_TO_EDGE_ENFORCED），adjustResize **不再压缩窗口**，键盘只是
     * 作为 ime inset 叠上来。于是键盘"已显示"（mInputShown=true、IME 窗口
     * 存在），却没有任何东西给它让位 —— 终端全屏画在它下面，键盘被自己的
     * 零高度可视区吃掉了，屏幕上只剩我们自己的工具条。
     *
     * 所以：SafeArea 仍只报设备画的（状态栏、挖孔、手势条），IME 的高度
     * 用 Native.onImeInsets 单独报，Go 把终端让开那一段 —— 与 iOS 上
     * UIKeyboardFrame 的角色相同，但那条路径在 Android 上从来没有过。
     */
    private fun reportSafeArea() {
        val insets = rootWindowInsets ?: return
        val d = resources.displayMetrics.density
        if (d <= 0f) return
        val bars = insets.getInsets(WindowInsets.Type.systemBars())
        val cut = insets.getInsets(WindowInsets.Type.displayCutout())
        fun dip(px: Int): Int = Math.round(px / d)
        // 🔴 键盘计入 bottom：在 targetSdk 36 的强制 edge-to-edge 下，
        // adjustResize 不再压缩窗口，IME 只是作为 inset 叠在内容上。不让位的
        // 话终端全屏画在键盘下面，而键盘的可视区被吃掉 —— "mInputShown=true
        // 但屏幕上没有键盘"就是这么来的。
        //
        // 用 isVisible + getInsets：动画期间 isVisible 已是真，而 getInsets
        // 给的是动画结束的目标高度，所以内容一次让到位，不会逐帧跳。
        //
        // 与手势条取较大值而不是相加：键盘弹起时它盖住手势条，两者不叠加。
        val ime = if (insets.isVisible(WindowInsets.Type.ime())) {
            insets.getInsets(WindowInsets.Type.ime()).bottom
        } else {
            0
        }
        Native.onSafeArea(
            dip(maxOf(bars.top, cut.top)),
            dip(maxOf(bars.right, cut.right)),
            dip(maxOf(bars.bottom, cut.bottom, ime)),
            dip(maxOf(bars.left, cut.left)),
        )
    }

    override fun onApplyWindowInsets(insets: WindowInsets): WindowInsets {
        super.onApplyWindowInsets(insets)
        // 不消费（不 return CONSUMED）：本 View 不做让位，只把值读出来交给
        // Go 侧，让位由 Go 的布局做。消费掉会让同一层级的其它 View 拿不到。
        reportSafeArea()
        return insets
    }

    // ─────────────────── 触摸 ───────────────────

    override fun onTouchEvent(event: MotionEvent): Boolean {
        // 整个事件交给 MygoInput：actionMasked、actionIndex、全部指针的 id 与
        // view 内坐标都在那一趟里过去（多指可见 —— 见文件头「多指」一段）。
        MygoInput.onTouchEvent(event)
        return true // 消费掉，避免系统再做手势判定
    }

    /**
     * 鼠标/触控板的滚轮走这里（onTouchEvent 收不到 ACTION_SCROLL）。
     * ACTION_SCROLL 同样是 MotionEvent，字段口径一致，交给同一条通道。
     */
    override fun onGenericMotionEvent(event: MotionEvent): Boolean {
        if (event.actionMasked == MotionEvent.ACTION_SCROLL) {
            MygoInput.onTouchEvent(event)
            return true
        }
        return super.onGenericMotionEvent(event)
    }

    // ─────────────────── 按键 ───────────────────

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        MygoInput.onKeyEvent(event)
        return true
    }

    override fun onKeyUp(keyCode: Int, event: KeyEvent): Boolean {
        MygoInput.onKeyEvent(event)
        return true
    }

    // ─────────────────── 文字输入（IME）────────────────────────────────────────
    // 真正的编辑状态在 Go 侧：本 View 不持有文本，只把系统输入法接到 MygoImeConnection。
    // 文本、光标、选中、预编辑都在 Go 侧那个可编辑元素的 TextInputClient 里
    // （internal/android/backend/textinput.go 是两边语义的翻译）。

    /** 本 View 是文字编辑器：系统据此才会为它建 InputConnection 并派发 IME 事件。 */
    override fun onCheckIsTextEditor(): Boolean = true

    /**
     * 把 Go 侧的 `InputOptions.Return` 映射成 [EditorInfo.imeOptions] 的那个字段。
     *
     * 索引顺序与 Go 侧 `nativeReturnKey` 一致。取不到值（没有可编辑元素、
     * 或元素没设 Return）时给 IME_ACTION_NONE：那是这里一直以来的行为，
     * 文本编辑器靠它拿到换行。
     */
    private fun returnAction(): Int = try {
        when (MygoIme.nativeReturnKey()) {
            1 -> EditorInfo.IME_ACTION_DONE
            2 -> EditorInfo.IME_ACTION_GO
            3 -> EditorInfo.IME_ACTION_NEXT
            4 -> EditorInfo.IME_ACTION_SEARCH
            5 -> EditorInfo.IME_ACTION_SEND
            6 -> EditorInfo.IME_ACTION_DONE // Android 没有 continue，用 done
            else -> EditorInfo.IME_ACTION_NONE
        }
    } catch (e: Throwable) {
        // 库还没加载（或正在退出）时不要因此崩掉一次建连接 —— Go 侧的
        // 「没有动作」就是这里的默认值。
        EditorInfo.IME_ACTION_NONE
    }

    override fun onCreateInputConnection(outAttrs: EditorInfo): InputConnection {
        // 光标/选中的初值从 Go 侧取；取不到（-1，表示当前没有可编辑元素）时给 0，
        // 输入法至少不会拿到越界的位置。之后每次编辑后输入法都会用
        // getTextBeforeCursor / getTextAfterCursor / getSelectedText 重新问。
        val start = MygoIme.nativeSelectionStart()
        val end = MygoIme.nativeSelectionEnd()
        outAttrs.initialSelStart = if (start >= 0) start else 0
        outAttrs.initialSelEnd = if (end >= 0) end else 0
        // 多行文本 + 关掉输入法自己的建议/自动纠正：文本、编辑策略与撤销历史属于内容
        // （Go 侧），输入法只负责送键与送候选。
        outAttrs.inputType = InputType.TYPE_CLASS_TEXT or
            InputType.TYPE_TEXT_FLAG_MULTI_LINE or
            InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
        // 不要全屏 IME。Return 的动作由元素自己说（ui.InputOptions.Return）：
        // 文本编辑器要的是换行（IME_ACTION_NONE），而终端要的是每条命令以回车
        // 结束 —— 设成 IME_ACTION_SEND 之类之后，回车键才会变成一个真正的
        // 「动作键」，由 performEditorAction 送回按键路径，而不是被输入法
        // 当成编辑自己消费掉。
        outAttrs.imeOptions = EditorInfo.IME_FLAG_NO_FULLSCREEN or EditorInfo.IME_ACTION_NONE // TEMP-TEST
        return MygoImeConnection(this)
    }

    /** 由 Activity 在 setContentView 之后调用，确保能收到按键事件。 */
    fun grabInputFocus() {
        requestFocus()
    }
}
