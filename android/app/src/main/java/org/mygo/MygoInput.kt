package org.mygo

import android.view.KeyEvent
import android.view.MotionEvent

/**
 * Android 输入到 Go 侧的入口：把一个**完整**的 MotionEvent / KeyEvent 交给
 * Go，而不是只交「action 所指的那一个指针」。
 *
 * ============================================================================
 * 为什么要有这个类（与 Native.java 的关系）
 * ============================================================================
 * Native.java 里的 nativeOnTouch(int, float, float, long) 是**单点**契约：
 * 它只能表达一个指针，多指手势的 pointerCount / 各指坐标 / pointer id 都传不
 * 下去（那份契约不动，见任务书）。本类声明**属于它自己的** native 方法，
 * 名字不与 Java_org_mygo_Native_* 相撞，于是同一份 .so 里两条通道并存：
 *
 *   Native     —— 面生命周期 + 旧的单点触摸/按键（骨架，未改）
 *   MygoInput  —— 整事件转发（本波新增，多指由此可见）
 *
 * 转发方向是一件事一个入口：MygoView 把触摸/按键交给本类，Go 侧
 * android/jni/jni_input.go 把这些 native 方法解码成普通 Go 值，
 * internal/android/backend/input.go 再把它们映射成 platform.SurfaceEvent /
 * platform.Key —— 那一层是**平台无关纯逻辑**，在桌面上用
 * `CGO_ENABLED=0 go test ./internal/android/backend/` 就能跑。
 *
 * ============================================================================
 * native 方法不许随便改（与 Go 侧 //export 逐字对齐）
 * ============================================================================
 * 方法名不带下划线，JNI 符号就是 Java_org_mygo_MygoInput_<方法名>，无需
 * 「_1」转义。Kotlin `object` 的成员在 JVM 上就是 org.mygo.MygoInput 的实例
 * 方法，不经过 companion 的 @JvmStatic 名字改写（Native.java 文件头记的那个
 * 坑：companion 里不带 @JvmStatic 会变成 MygoInput$Companion）。
 *
 * 🔴 这三个 external fun **必须是 public**：验收用
 *    javap -classpath <kotlin-classes> -s org.mygo.MygoInput
 *    （不带 -p）打印签名，private 的成员根本不会出现在输出里，核对就落空。
 *    internal 也不行：Kotlin 会给 internal 成员名加 $模块名 后缀，JNI 符号就变了。
 *
 * ============================================================================
 * 坐标口径
 * ============================================================================
 * getX(i) / getY(i) 返回的**本来就是相对本 View 左上角**的坐标（view 内坐标），
 * 不是屏幕坐标 —— 绝不要换成 getRawX()/getRawY()。Go 侧拿到的就是这里的像素，
 * 再由 MygoInput.onDisplayInfo 报过去的 densityDpi 换算成 DIP
 * （platform.SurfaceEvent.X/Y 的口径，1 DIP = dpi/160 像素）。
 *
 * ============================================================================
 * 多指为什么「表达得出来」了
 * ============================================================================
 * 一次 native 调用带整组指针（ids / xs / ys 三个等长数组 + actionIndex），
 * 所以「第二根手指按下」在 Go 侧是一个完整事实，不需要按 id 逐指调用再拼。
 * Go 侧把整组指针作为 platform.SurfaceEvent 之外的一份 payload 产出
 * （backend.PointerState），契约一扩就能接上 —— 方案见 T32.md。
 */
object MygoInput {

    init {
        // 库名必须逐字是 "mygo"（与 Native.java 一致）。Native 的静态初始化
        // 块也会加载同一份库；System.loadLibrary 对同一 classloader 是幂等的。
        System.loadLibrary("mygo")
    }

    // ───────────────────────── native 契约（与 Go 侧 //export 逐字对齐）─────────────────────────
    // 参数类型故意只用 JNI 原始类型与数组（Int / FloatArray / Float / Long），
    // 不让任何 Android 类穿过 native 边界：Go 侧不认识 Android 类型，也不该认识。

    /**
     * 屏幕密度（resources.displayMetrics.densityDpi），每英寸多少 dot。
     * Go 侧据此把 view 像素换算成 DIP。没有它时按 160 处理（1:1）。
     *
     * @param refreshHz 当前显示器模式的刷新率（Hz）。Go 侧用它给帧的节奏
     *   定基准；面板会自己降档（静止 60Hz、触摸 120Hz），所以这里报的是
     *   **这一刻这一档**，不是面板的上限。拿不到时传 0，Go 侧按"未知"处理。
     */
    external fun nativeOnDisplayInfo(dpi: Int, refreshHz: Float)

    /**
     * 一个完整的 MotionEvent。
     *
     * @param action      event.actionMasked（低 8 位是 ACTION_*，高位是指针下标）
     * @param pointerIds  event.getPointerId(i)，**id 而非 index**：id 在一次手势内
     *                    稳定，index 会随手指起落前后移位
     * @param xs / ys     event.getX(i) / getY(i)，view 内像素，与 pointerIds 等长
     * @param actionIndex event.getActionIndex()：本次 action 作用于哪个指针
     * @param buttonState event.getButtonState()：鼠标按键（手指为 0）
     * @param metaState   event.getMetaState()：修饰键
     * @param scrollX/Y   ACTION_SCROLL 时 event.getAxisValue(AXIS_HSCROLL/AXIS_VSCROLL)
     * @param eventTimeMs event.getEventTime()：Android 自己的时钟，Go 侧用它数连击
     */
    external fun nativeOnTouchBatch(
        action: Int,
        pointerIds: IntArray,
        xs: FloatArray,
        ys: FloatArray,
        actionIndex: Int,
        buttonState: Int,
        metaState: Int,
        scrollX: Float,
        scrollY: Float,
        eventTimeMs: Long,
    )

    /**
     * 一个完整的 KeyEvent。
     *
     * @param unicodeChar event.unicodeChar：这个键打出的字符（已含键盘布局与 Shift），
     *                    打不出字符时为 0 —— 这是 Go 侧不用自带布局表就能区分
     *                    「文本」与「纯按键」的依据
     * @param repeatCount event.repeatCount：大于 0 表示自动重复
     */
    external fun nativeOnKeyEvent(
        keyCode: Int,
        action: Int,
        metaState: Int,
        unicodeChar: Int,
        repeatCount: Int,
        eventTimeMs: Long,
    )

    // ───────────────────────── 包内转发入口（非 native，可自由改）─────────────────────────

    /** 由 MygoView 在 surfaceChanged 时调用（也与 onSizeChanged 同源）。 */
    fun onDisplayInfo(dpi: Int, refreshHz: Float) {
        if (dpi > 0) {
            nativeOnDisplayInfo(dpi, refreshHz)
        }
    }

    /**
     * 把一个 MotionEvent 整组转发下去：所有指针的 id 与坐标一趟带过去。
     *
     * 触摸与 ACTION_SCROLL（鼠标/触控板滚轮）走的是同一个方法：两者都是
     * MotionEvent，字段口径一致，分开反而要复制一遍取数组的代码。
     */
    fun onTouchEvent(event: MotionEvent) {
        val count = event.pointerCount
        val ids = IntArray(count)
        val xs = FloatArray(count)
        val ys = FloatArray(count)
        for (i in 0 until count) {
            // getX(i)/getY(i) 是 view 内坐标（见文件头「坐标口径」）。
            ids[i] = event.getPointerId(i)
            xs[i] = event.getX(i)
            ys[i] = event.getY(i)
        }
        nativeOnTouchBatch(
            event.actionMasked,
            ids,
            xs,
            ys,
            event.actionIndex,
            event.buttonState,
            event.metaState,
            event.getAxisValue(MotionEvent.AXIS_HSCROLL),
            event.getAxisValue(MotionEvent.AXIS_VSCROLL),
            event.eventTime,
        )
    }

    /**
     * 把一个 KeyEvent 转发下去。onKeyDown/onKeyUp 收到的事件里 action 已经是
     * KeyEvent.ACTION_DOWN / ACTION_UP，不需要再判一遍。
     */
    fun onKeyEvent(event: KeyEvent) {
        nativeOnKeyEvent(
            event.keyCode,
            event.action,
            event.metaState,
            event.unicodeChar,
            event.repeatCount,
            event.eventTime,
        )
    }
}
