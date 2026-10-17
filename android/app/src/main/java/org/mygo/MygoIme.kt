package org.mygo

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.view.KeyEvent
import android.view.View
import android.view.inputmethod.BaseInputConnection
import android.view.inputmethod.InputMethodManager

/**
 * Android 文字输入（IME）在 Java 侧的落点。
 *
 * ============================================================================
 * 为什么 native 声明挂在本对象（顶层 object）上，而不是 companion object
 * ============================================================================
 * 契约要求 JNI 符号逐字是 `Java_org_mygo_MygoIme_<方法名>`。Kotlin 的
 * `external` 成员如果写在 companion object 里，JVM 类名是
 * `org.mygo.MygoIme$Companion`，符号会变成
 * `Java_org_mygo_MygoIme_00024Companion_<方法名>`（这正是 Native.java 文件头注释里
 * 那条坑的同一个原因）。**顶层 object** 的 JVM 类名就是 `org.mygo.MygoIme`，
 * 符号逐字对上，零推断风险。
 *
 * ============================================================================
 * 两个方向（谁调用谁）
 * ============================================================================
 * · Java → Go（编辑与查询，同步）：系统输入法调 [MygoImeConnection] 的方法，
 *   后者调本对象的 native 方法，Go 侧（android/jni/jni_textinput.go）把调用
 *   投到后端的 pump goroutine 上并**等**它（回调必须在 pump 线程上跑），拿到结果
 *   再返回。因此 `getTextBeforeCursor` 这类方法必须**同步**返回。
 * · Go → Java（唤起/收起键盘）：Go 侧的可编辑元素拿到/失去键盘时，从它的 pump
 *   线程调 [setSoftKeyboard]。JS 之外只有 Go 知道 mygo 的哪个元素变成了可编辑，
 *   而只有 Java 能开键盘，所以这条方向绕不开。
 *
 * ⚠️ [setSoftKeyboard] **只投递、立即返回**，不在里面同步做 InputMethodManager 的
 * 事：否则 pump 会阻塞在 Java 上，而 Java 可能正阻塞在某个 InputConnection 方法里
 * 等 pump 回答案 —— 两边互等就是死锁。
 */
object MygoIme {

    init {
        // 库名必须逐字是 "mygo"（与 Native.java 一致）。不能只靠 Native 的静态块：
        // MygoView 的构造器里就会调 bindView，那一刻可能尚未碰过 Native 类
        // ⇒ 库还没被加载 ⇒ UnsatisfiedLinkError（真机上实测崩过）。
        // System.loadLibrary 对同一 classloader 幂等，重复加载是安全的（同 MygoInput）。
        System.loadLibrary("mygo")
    }

    /** 主线程 handler。Go 从 pump 线程调进来，InputMethodManager 必须在主线程用。 */
    private val main = Handler(Looper.getMainLooper())

    /**
     * 把承载文字输入的 View 交给 Go 侧（Java → Go，一次性登记）。
     *
     * 由 `MygoView.init` 调用，**不是**等第一次 onCreateInputConnection 才调：
     * 可编辑元素刚拿到键盘时 Go 就要能开键盘，而那一刻还没有 InputConnection
     * —— 系统只有在被要求弹键盘之后才会来要一个。
     */
    external fun bindView(view: View)

    /**
     * Go → Java 方向：`active` 为真时唤起软键盘，为假时收起。
     *
     * 由 Go 侧 `internal/android/backend/textinput.go` 的 `Ime.OnActive` 触发，
     * 在 Go 的 pump 线程上被调用。这里只投递到主线程就返回（理由见文件头注释）。
     */
    fun setSoftKeyboard(view: View, shown: Boolean) {
        main.post {
            val imm = view.context.getSystemService(Context.INPUT_METHOD_SERVICE) as? InputMethodManager
                ?: return@post
            if (shown) {
                imm.showSoftInput(view, InputMethodManager.SHOW_IMPLICIT)
            } else {
                imm.hideSoftInputFromWindow(view.windowToken, 0)
            }
        }
    }

    // ── Java → Go：编辑回调（返回 true 表示这次编辑真的发生了）──────────────
    // 返回值语义照 Android 契约：false 是「没有可编辑的目标 / 这个动作没做成」，
    // 输入法据此决定后续行为。不要一律返回 true。

    external fun nativeSetComposingText(text: String, newCursorPosition: Int): Boolean

    external fun nativeCommitText(text: String, newCursorPosition: Int): Boolean

    external fun nativeFinishComposingText(): Boolean

    external fun nativeDeleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean

    external fun nativeSetSelection(start: Int, end: Int): Boolean

    // ── Java → Go：查询（返回 null 表示「没有可编辑的目标 / 没有文本」）──────

    external fun nativeTextBeforeCursor(n: Int): String?

    external fun nativeTextAfterCursor(n: Int): String?

    external fun nativeSelectedText(): String?

    external fun nativeSelectionStart(): Int

    /**
     * 光标处的键盘 Return 动作，作为 [EditorInfo.imeOptions] 的索引：
     * 0 无动作（IME_ACTION_NONE），1 done，2 go，3 next，4 search，5 send，6 continue。
     *
     * 顺序必须与 Go 侧 `Java_org_mygo_MygoIme_nativeReturnKey` 一致。
     * 值为空（元素没设 InputOptions.Return）时返回 0 —— 保持原来的行为。
     *
     * 🔴 为什么需要它：MygoView 硬编码 IME_ACTION_NONE，于是回车永远是
     * 「换行」而不是「动作」，输入法会自己把回车吃掉。终端要的是每条命令
     * 都以回车结束，所以它设 ReturnSend，这里才把回车变成真正的动作键。
     */
    external fun nativeReturnKey(): Int

    external fun nativeSelectionEnd(): Int
}

/**
 * 承载 mygo 光标与选中的 [android.view.inputmethod.InputConnection]。
 *
 * 它不持有文本：文本、选中、预编辑都在 Go 侧那个可编辑元素的
 * `TextInputClient` 里（`internal/android/backend/textinput.go` 是两边语义的翻译），
 * 这里只做「系统输入法 → native → Go」的转发与返回值语义。
 *
 * 参数类型写成 [View] 而不是 `MygoView`：本类只用到 View 的
 * `dispatchKeyEvent`，这样它与 View 的具体实现解耦（MygoView 只需
 * `onCreateInputConnection` 里 `return MygoImeConnection(this)` 即可）。
 */
class MygoImeConnection(private val view: View) : BaseInputConnection(view, true) {

    // ── 组合（预编辑）─────────────────────────────────────────────────────
    // 中文输入法的一次「拼音 → 候选 → 上屏」就是：
    //   setComposingText 多次（每次拼音多一个字母）→ commitText 一次。
    // 状态机必须能处理这种组合，见 internal/android/backend/textinput.go。

    /** text 允许为 null（有的输入法会传 null），null 按空串处理。 */
    override fun setComposingText(text: CharSequence?, newCursorPosition: Int): Boolean =
        MygoIme.nativeSetComposingText(text?.toString() ?: "", newCursorPosition)

    override fun commitText(text: CharSequence?, newCursorPosition: Int): Boolean =
        MygoIme.nativeCommitText(text?.toString() ?: "", newCursorPosition)

    override fun finishComposingText(): Boolean = MygoIme.nativeFinishComposingText()

    // ── 删除与选中 ────────────────────────────────────────────────────────

    override fun deleteSurroundingText(beforeLength: Int, afterLength: Int): Boolean =
        MygoIme.nativeDeleteSurroundingText(beforeLength, afterLength)

    /**
     * 折叠请求（start == end）是移动光标；**非折叠请求返回 false**：
     * mygo 的 `TextInputClient` 没有「设置选中」这一步，唯一能表达的写法是
     * 把该区间替换成空串 —— 那等于把输入法想选中的文本删掉。宁可如实说
     * 「没做成」（Android 契约就是 false 表示没做成），也不静默删文本。
     * 详见 `internal/android/backend/textinput.go` 的 `SetSelection`。
     */
    override fun setSelection(start: Int, end: Int): Boolean = MygoIme.nativeSetSelection(start, end)

    // ── 查询（输入法要用这些读光标前后的上下文）────────────────────────────
    // `flags` 里的 GET_TEXT_WITH_STYLES 被忽略：mygo 这一层只暴露纯文本，
    // 返回带样式的东西也没有样式可言。返回 null 即 Android 的「没有文本」。

    override fun getTextBeforeCursor(n: Int, flags: Int): CharSequence? = MygoIme.nativeTextBeforeCursor(n)

    override fun getTextAfterCursor(n: Int, flags: Int): CharSequence? = MygoIme.nativeTextAfterCursor(n)

    override fun getSelectedText(flags: Int): CharSequence? = MygoIme.nativeSelectedText()

    // ── 按键 ──────────────────────────────────────────────────────────────

    /**
     * 输入法注入的按键（回车、删除、方向键…）走 View 的按键路径，
     * 于是和硬件键完全同一条路：`MygoView.onKeyDown` → `Native.onKey` → Go 侧
     * `backend.OnKey`。**不要**另开一条 native 入口，那会让同一个键有两个来源。
     */
    override fun sendKeyEvent(event: KeyEvent): Boolean = view.dispatchKeyEvent(event)

    /**
     * 动作键（IME_ACTION_SEND / GO / DONE …）按下时走这里。
     *
     * 🔴 这是终端能执行命令的关键。把 Return 设成一个 action 之后，回车键
     * 就不再产生换行，而是变成一个「动作」交给编辑器；终端要的不是被叫醒
     * 一次，是收到一个回车。所以动作一律还原成 [KeyEvent.KEYCODE_ENTER]，
     * 送回 View 的按键路径 —— 与 [sendKeyEvent] 同一条路，Go 侧密钥编码器
     * 才知道它是 Enter（终端里那是 CR，不是一个换行字符）。
     *
     * 编辑器（TextInput/TextArea）不会走到这里：它们的 Return 是
     * IME_ACTION_NONE，输入法直接把换行作为文字提交过来。
     *
     * 返回 true：动作已被消费，Android 不必再去点「下一项」或收键盘。
     */
    override fun performEditorAction(editorAction: Int): Boolean {
        val down = KeyEvent(KeyEvent.ACTION_DOWN, KeyEvent.KEYCODE_ENTER)
        val up = KeyEvent(KeyEvent.ACTION_UP, KeyEvent.KEYCODE_ENTER)
        view.dispatchKeyEvent(down)
        view.dispatchKeyEvent(up)
        return true
    }
}
