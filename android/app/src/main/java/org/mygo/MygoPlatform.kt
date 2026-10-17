package org.mygo

import android.app.Activity
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.ActivityNotFoundException
import android.content.BroadcastReceiver
import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.provider.MediaStore
import android.graphics.Rect
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.View
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityManager
import android.view.accessibility.AccessibilityNodeInfo
import android.view.accessibility.AccessibilityNodeProvider
import org.json.JSONArray
import org.json.JSONObject
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Android 平台能力的 Java 侧实现：剪贴板、打开 URL、通知、无障碍。
 *
 * =============================================================================
 * 与 Go 侧的分工（先说清楚，后面每一段都按这个走）
 * =============================================================================
 * · **Java → Go**：本 object 里 3 个 `external` 方法（nativeAttach /
 *   nativeNotificationClicked / nativeAccessibilityAction）。它们的 JNI 符号名由类名 +
 *   方法名决定，所以**类名必须逐字是 `org.mygo.MygoPlatform`**，且成员必须挂在
 *   **类本身**上而不能挂在 `companion object` 下 —— 后者会把 native 方法变成
 *   `MygoPlatform$Companion` 的成员，符号随之变成
 *   `Java_org_mygo_MygoPlatform_00024Companion_nativeXxx`，与 Go 侧 `//export` 对不上。
 *   用 Kotlin `object` 声明的 `external fun` 正好落在类上，实例方法 ⇒
 *   Go 侧第二参数是 jobject（不是 jclass），符号仍是 `Java_org_mygo_MygoPlatform_<方法名>`。
 *   （Native.java 走的是 Java 的 `private static native`，那是另一套：它的 6 个符号是
 *   `Java_org_mygo_Native_*`，与本文件无关，也不要动。）
 *
 * · **Go → Java**：下面那些**普通方法**（readClipboardText / openExternalUrl /
 *   postNotification / setAccessibilityTree ...）。Go 侧用 JNI GetMethodID 按
 *   **方法名 + 签名**取到它们再调用，名字和签名逐字写在
 *   `android/jni/jni_platform.go` 的 `mygo_platform_attach` 表里；
 *   两边必须一起改。**这些方法必须是 public**：JNI 的 GetMethodID 对 private
 *   成员在各家 ART 上行为不保证，public 是最省事的正确做法。
 *
 * =============================================================================
 * 🔴 字符串一律用 ByteArray(UTF-8) 收发，不用 String
 * =============================================================================
 * JNI 的 NewStringUTF / GetStringUTFChars 走的是 **modified UTF-8**：它不能带 NUL，
 * 且 BMP 之外的字符（emoji、不少 CJK 扩展字）在两侧会被拆成代理对再拼错。剪贴板正是
 * 这类字符最容易出现的地方，所以本文件所有跨 JNI 的文本参数/返回值都是 `ByteArray?`
 * （null = 该表示“没有”），由本文件 `String(bytes, Charsets.UTF_8)` 解码。
 * 这是有意为之，不是偷懒。
 *
 * =============================================================================
 * 🔴 线程：Go 侧从 pump 线程调进来，UI 相关一律 post 到主线程
 * =============================================================================
 * Go 调过来的线程是 Go 的（不是 Android 主线程）。ClipboardManager / NotificationManager
 * / startActivity 从任意线程调都合法，但 **View 上的操作**（announceForAccessibility、
 * sendAccessibilityEvent、读 getLocationOnScreen）必须在主线程，所以本文件用 [onUi]
 * 统一处理：已经在主线程就直接执行，否则 Handler.post。
 *
 * =============================================================================
 * 本波做到什么程度（不要当成都做完了）
 * =============================================================================
 * · 剪贴板：读写文本 + HTML（ClipData 一个 item 同时带两个表示），✅ 真机可验。
 * · 打开 URL：ACTION_VIEW，✅ 真机可验；scheme 白名单在 Kotlin 侧**再查一遍**（见下面）。
 * · 通知：渠道 + POST_NOTIFICATIONS 运行时权限 + 点击回传，✅ 真机可验（需 Manifest 权限）。
 * · 无障碍：AccessibilityNodeProvider + 节点 JSON→AccessibilityNodeInfo + 动作回传 +
 *   announcement，代码完整；但**上游还差两处接线**（见文件末 mygo_platform_attach 需要的
 *   补丁清单）：① Go 侧 surface.UpdateAccessibility 未接（surface.go 是别人的面）；
 *   ② Android 侧还没有谁把 AccessTree 交给后端。⇒ 现状是「节点可被读」这条链路的
 *   Java 半段全部就位、Java 侧可单测（喂 JSON 就能拿节点），端到端未通。**不夸大。**
 */
object MygoPlatform {

    init {
        // 同 MygoIme：install() 与通知 receiver 都可能在任何 Native.* 之前被调用，
        // 不能只靠 Native 的静态块（幂等，同 MygoInput）。
        System.loadLibrary("mygo")
    }

    // ─────────────────────────── 状态 ───────────────────────────

    /** 宿主 Activity；install 时记下，UI 线程外的操作靠它 post。 */
    @Volatile
    private var activity: Activity? = null

    /** 承载 Go 渲染的 View（SurfaceView）；无障碍 provider 挂在它上面。 */
    @Volatile
    private var host: View? = null

    /** 主线程 Handler；懒建，避免 install 之前就碰 Looper。 */
    private val mainHandler by lazy { Handler(Looper.getMainLooper()) }

    private const val CHANNEL_ID = "org.mygo.default"
    private const val CHANNEL_ID_SILENT = "org.mygo.silent"
    private const val NOTIFICATION_ID = 0
    private const val ACTION_NOTIFICATION_CLICK = "org.mygo.NOTIFICATION_CLICK"
    private const val EXTRA_NOTIFICATION_ID = "org.mygo.NOTIFICATION_ID"

    // ─────────────────────────── 安装 ───────────────────────────

    /**
     * 由 Activity 在 onCreate 里调用一次：记下 Context 与 View、建通知渠道、通知 Go 侧
     * 「Java 侧就绪」。
     *
     * `nativeAttach()` 是本文件与 Go 侧唯一一次「握手」：Go 侧要靠它拿到 JavaVM，之后
     * Go 才调得动本文件的那些方法。**必须在任何平台能力被使用之前调用**，否则 Go 侧报
     * `ErrPlatformBridge`（而不是静默失败）。第二次调用是幂等的，但换 Activity 会让旧引用
     * 一直留着，所以这里只记最新的。
     */
    fun install(activity: Activity, host: View) {
        this.activity = activity
        this.host = host
        createNotificationChannel(activity)
        host.accessibilityDelegate = MygoAccessibilityDelegate()
        nativeAttach()
    }

    /** Activity 销毁时调用：放开对 Activity/View 的引用。Go 侧不需要额外的 detach。 */
    fun uninstall() {
        activity = null
        host = null
        clearAccessibilityTree()
    }

    // ─────────────────────────── 剪贴板 ───────────────────────────

    /**
     * 读剪贴板文本，null 表示「没有文本表示」。
     *
     * 🔴 **Android 10（API 29）起的后台读取限制**：只有当前获得焦点的应用能读剪贴板，
     * 后台应用拿到的是 null，而且**与「剪贴板为空」在 API 上无法区分**
     * （hasPrimaryClip() 同样返回 false）。系统日志会写：
     *   `W/ClipboardService: Denying clipboard access to <pkg>, application is not in
     *    focus nor is it a system service for user 0`
     * 这不是本实现的 bug，是平台规则：调用方要在自己的窗口有焦点时读。
     */
    fun readClipboardText(): ByteArray? {
        val item = primaryClipItem() ?: return null
        val text = item.text ?: return null
        return text.toString().toByteArray(Charsets.UTF_8)
    }

    /** 读剪贴板的 HTML 表示，null 表示没有。 */
    fun readClipboardHtml(): ByteArray? {
        val item = primaryClipItem() ?: return null
        val html = item.htmlText ?: return null
        return html.toByteArray(Charsets.UTF_8)
    }

    /**
     * 剪贴板 clip 自己声明的 MIME 类型，每行一个、以 `\n` 拼接，null 表示读不到。
     *
     * 为什么是「一行一个的字节串」而不是 `Array<String>`：MIME 类型（RFC 6838）不含
     * 换行，而 Go 侧只要一种 JNI 形状（byte[]），返回字符串数组会多出一整套
     * GetObjectArrayElement/GetStringUTFChars 的胶水。这是刻意的取舍。"
     */
    fun clipboardMimeTypes(): ByteArray? {
        val cm = clipboardManager() ?: return null
        val description: ClipDescription = cm.primaryClipDescription ?: return null
        val mimes = ArrayList<String>(description.mimeTypeCount)
        for (i in 0 until description.mimeTypeCount) {
            val mime = description.getMimeType(i)
            if (!mime.isNullOrEmpty()) mimes.add(mime)
        }
        return mimes.joinToString("\n").toByteArray(Charsets.UTF_8)
    }

    /**
     * 写剪贴板：一个 clip、一个 item，可同时带 text/plain 与 text/html。
     *
     * 两个参数都可为 null（= 该表示不存在），但**至少一个非 null**；两个都是 null 时返回
     * false（Go 侧的空 Data 走的是 clearClipboard）。空字符串是合法值，与 null 语义不同，
     * 这一点由 Go 侧的 hasPlain/hasHTML 显式传递，本方法不做任何 trim。
     */
    fun writeClipboard(plain: ByteArray?, html: ByteArray?): Boolean {
        val cm = clipboardManager() ?: return false
        val text = plain?.let { String(it, Charsets.UTF_8) }
        val markup = html?.let { String(it, Charsets.UTF_8) }
        if (text == null && markup == null) return false
        return try {
            val mimes = ArrayList<String>(2)
            if (text != null) mimes.add("text/plain")
            if (markup != null) mimes.add("text/html")
            val item = ClipData.Item(text, markup)
            cm.setPrimaryClip(ClipData(ClipDescription("mygo", mimes.toTypedArray()), item))
            true
        } catch (e: RuntimeException) {
            // setPrimaryClip 在异常输入下会抛（例如 SecurityException：后台应用在
            // 某些 OEM 上不允许写）；返回 false 让 Go 侧报错，而不是把异常留在 JNI 线程上。
            false
        }
    }

    /** 清空剪贴板。 */
    fun clearClipboard(): Boolean {
        val cm = clipboardManager() ?: return false
        return try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                cm.clearPrimaryClip()
            } else {
                // API 24..27 没有 clearPrimaryClip：置一个空文本 clip 等价于「没有东西」。
                cm.setPrimaryClip(ClipData.newPlainText("", ""))
            }
            true
        } catch (e: RuntimeException) {
            false
        }
    }

    private fun clipboardManager(): ClipboardManager? {
        val ctx = activity ?: return null
        return ctx.getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
    }

    private fun primaryClipItem(): ClipData.Item? {
        val cm = clipboardManager() ?: return null
        val clip = try {
            cm.primaryClip
        } catch (e: RuntimeException) {
            // 后台读取被拒时这里可能是 SecurityException（各家 ROM 行为不同），
            // 语义与 null 一致：「读不到」。
            return null
        }
        if (clip == null || clip.itemCount == 0) return null
        return clip.getItemAt(0)
    }

    // ─────────────────────────── 打开 URL ───────────────────────────

    /**
     * 用 ACTION_VIEW 打开一个 URL。返回 0 = 已交给系统；1 = 没有应用能处理；
     * 2 = 被拒绝（非法 URL / 系统拒绝启动）。
     *
     * 🔴 **安全边界（与 Go 侧 validateExternalURL 是两道独立的检查）**
     * 参数来自不可信输入（可能是页面给的），所以：
     *  · **scheme 白名单**：只允许 http / https / mailto / tel。file:// content://
     *    intent:// android-app:// package: market: data: javascript: blob: 一律拒绝 ——
     *    这些 scheme 指向的不是网页而是**组件或本机资源**，直接拼进 Intent 就等于把
     *    「打开网页」变成「启动某个组件 / 读某个文件」。
     *  · **不 setPackage / 不 setComponent**：Intent 保持隐式，由系统挑处理器；显式
     *    指定包名会把 URL 变成「启动指定应用」的手段。
     *  · **不加任何 extras、不带 clipData**：不给接收方留第二个未经检查的字符串。
     *  · http/https 额外加 CATEGORY_BROWSABLE，让只有声明了该 category 的浏览器类应用
     *    能收到；mailto/tel 不加（邮件/拨号应用通常不声明 BROWSABLE，加了会解析不到）。
     *  · 用 `Uri.parse` 之前先逐字符拒绝控制字符与首尾空白，避免 "https://x\n…" 这类
     *    靠换行绕过后续校验的形状。
     *
     * 🌐 注：Android 11+ 的**包可见性**（package visibility）会影响
     * `resolveActivity()` 这类查询；本实现**不查询**、直接 startActivity 并捕获
     * ActivityNotFoundException，所以**不需要** `<queries>` 声明。请不要为了「先判断有没有
     * 浏览器」而改成 resolveActivity —— 那才会需要 Manifest 补丁，且在这里没有收益。
     */
    fun openExternalUrl(url: ByteArray): Int {
        val ctx = activity ?: return 2
        val raw = String(url, Charsets.UTF_8)
        val scheme = validatedScheme(raw) ?: return 2
        val intent = Intent(Intent.ACTION_VIEW, Uri.parse(raw))
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (scheme == "http" || scheme == "https") {
            intent.addCategory(Intent.CATEGORY_BROWSABLE)
        }
        return try {
            ctx.startActivity(intent)
            0
        } catch (e: ActivityNotFoundException) {
            1
        } catch (e: SecurityException) {
            2
        }
    }

    /**
     * 与 Go 侧 `validateExternalURL` 同规则的第二道检查：返回小写 scheme，非法返回 null。
     *
     * 两道检查存在的原因不同：Go 侧那道是契约的一部分（不可信输入不许变成 Intent），
     * 这一道是「真正构造 Intent 的进程不许跳过检查」。两边任一改动都要同步改另一边。
     */
    private fun validatedScheme(raw: String): String? {
        if (raw.isEmpty()) return null
        if (raw.any { it == '\u0000' || it == '\r' || it == '\n' || it == '\t' }) return null
        if (raw != raw.trim()) return null
        val colon = raw.indexOf(':')
        if (colon <= 0) return null
        val scheme = raw.substring(0, colon).lowercase()
        if (scheme !in ALLOWED_SCHEMES) return null
        val rest = raw.substring(colon + 1)
        when (scheme) {
            "http", "https" -> {
                // 必须形如 scheme://host；"https:" 或 "https:///" 没有可加载的主机。
                if (!rest.startsWith("//")) return null
                val host = rest.substring(2).substringBefore('/').substringBefore('?').substringBefore('#')
                if (host.isEmpty()) return null
            }
            "mailto", "tel" -> if (rest.isEmpty()) return null
        }
        return scheme
    }

    private val ALLOWED_SCHEMES = setOf("http", "https", "mailto", "tel")

    // ─────────────────────────── 通知 ───────────────────────────

    /**
     * 当前是否允许发通知。
     *
     * Android 13（API 33）起是 **POST_NOTIFICATIONS 运行时权限**；13 以下这个权限在平台上
     * 根本不存在（checkSelfPermission 会返回 DENIED），此时看
     * NotificationManager.areNotificationsEnabled()（用户可以在设置里关掉整个渠道）。
     * 两条都查，才是「真的能不能发出来」。
     */
    fun notificationsAllowed(): Boolean {
        val ctx = activity ?: return false
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val granted = ctx.checkSelfPermission(Manifest_permission_POST_NOTIFICATIONS()) ==
                PackageManager.PERMISSION_GRANTED
            if (!granted) return false
        }
        val nm = notificationManager(ctx) ?: return false
        return nm.areNotificationsEnabled()
    }

    /**
     * 发一条通知。返回 0 = 已交给系统；1 = 权限没给（调用方应报
     * platform.ErrNotificationsDenied）；2 = 其它失败。
     *
     * 🔴 **Android 13+ 没权限时 notify() 会被系统接受并丢弃**，不抛异常、不返回错误 ——
     * 这正是「静默失败」的来源。所以这里在发之前显式查权限，缺权限就返回 1，让 Go 侧
     * 把 ErrNotificationsDenied 报给调用方，而不是让应用以为发成功了。
     */
    fun postNotification(id: ByteArray, title: ByteArray, body: ByteArray, silent: Boolean): Int {
        val ctx = activity ?: return 2
        val nm = notificationManager(ctx) ?: return 2
        if (!notificationsAllowed()) return 1
        val tag = String(id, Charsets.UTF_8)
        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            // 静默与否在 8.0+ 由**渠道**决定，不能按条覆盖，所以两条渠道各建一个
            // （见 createNotificationChannel）：否则用户设了「有声音」，任何一条都静不了。
            Notification.Builder(ctx, if (silent) CHANNEL_ID_SILENT else CHANNEL_ID)
        } else {
            Notification.Builder(ctx)
        }
        builder
            .setContentTitle(String(title, Charsets.UTF_8))
            .setContentText(String(body, Charsets.UTF_8))
            .setAutoCancel(true)
            .setSmallIcon(smallIcon(ctx))
            .setContentIntent(clickIntent(ctx, tag))
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            // 8.0 以下没有渠道：静默 = 不要声音、不要震动、不要灯。
            @Suppress("DEPRECATION")
            if (silent) {
                builder.setSound(null).setDefaults(0)
            } else {
                builder.setDefaults(Notification.DEFAULT_SOUND)
            }
        }
        return try {
            // tag = 应用给的 id，int id 恒为 0：由 tag 区分不同通知，避免把字符串 id
            // 哈希成 int 之后碰撞。
            nm.notify(tag, NOTIFICATION_ID, builder.build())
            0
        } catch (e: RuntimeException) {
            2
        }
    }

    /** 撤销某条通知（tag 就是原通知的 id）。 */
    fun cancelNotification(id: ByteArray): Boolean {
        val ctx = activity ?: return false
        val nm = notificationManager(ctx) ?: return false
        nm.cancel(String(id, Charsets.UTF_8), NOTIFICATION_ID)
        return true
    }

    /** 撤销本应用的所有通知。 */
    fun cancelAllNotifications(): Boolean {
        val ctx = activity ?: return false
        val nm = notificationManager(ctx) ?: return false
        nm.cancelAll()
        return true
    }

    /**
     * 请求通知权限（Android 13+）。**只有 Activity 能弹运行时权限对话框**，所以这个方法
     * 必须由 Activity 调用（见产出里的 MygoActivity 补丁）。13 以下什么都不做：那个版本
     * 没有这个权限可请求。
     */
    fun requestNotificationPermission(requestCode: Int) {
        val ctx = activity ?: return
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        if (notificationsAllowed()) return
        ctx.requestPermissions(arrayOf(Manifest_permission_POST_NOTIFICATIONS()), requestCode)
    }

    /**
     * 通知渠道（Android 8+ 必须建，否则 notify() 静默什么都不显示 —— 又一个静默失败点）。
     *
     * 建**两条**：默认音的一条、静默的一条。8.0 之后一条通知有没有声音由**渠道**决定，
     * 按条设置无效（framework 的 Notification.Builder 连 setSilent 都没有，那是
     * AndroidX 的 NotificationCompat 才有的），所以「静默通知」只能靠换渠道实现。
     * 渠道一旦创建，重要性就只能由用户在设置里改，所以这里不替用户决定，用默认重要性。
     */
    private fun createNotificationChannel(ctx: Context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = notificationManager(ctx) ?: return
        if (nm.getNotificationChannel(CHANNEL_ID) == null) {
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "mygo", NotificationManager.IMPORTANCE_DEFAULT)
            )
        }
        if (nm.getNotificationChannel(CHANNEL_ID_SILENT) == null) {
            nm.createNotificationChannel(
                NotificationChannel(
                    CHANNEL_ID_SILENT,
                    "mygo (silent)",
                    NotificationManager.IMPORTANCE_LOW
                )
            )
        }
    }

    private fun notificationManager(ctx: Context): NotificationManager? =
        ctx.getSystemService(Context.NOTIFICATION_SERVICE) as? NotificationManager

    /**
     * 通知的小图标。
     *
     * 通知**必须**有 smallIcon，否则 Android 会拒绝显示（并且不报错）。宿主工程没有自带
     * 图标资源，所以用应用自己的图标（applicationInfo.icon）；取不到时退到系统图标。
     * 真要上架时应当换成一张单色通知图标并加进 res/（Manifest/res 不归本条线）。
     */
    private fun smallIcon(ctx: Context): Int {
        val icon = ctx.applicationInfo?.icon ?: 0
        return if (icon != 0) icon else android.R.drawable.ic_dialog_info
    }

    /**
     * 点击通知的 PendingIntent：指向本文件里的 [NotificationReceiver]。
     *
     * · 用 **setData** 带上 id：PendingIntent 的「同一个」判定不看 extras，只按
     *   action/data/component 等比较；只塞 extra 的话不同通知会互相覆盖，点谁都回同一个 id。
     * · **FLAG_IMMUTABLE**：Android 12+ 强制要求显式可变性（缺了会抛
     *   IllegalArgumentException），本场景不需要接收方改内容，所以不可变。
     * · 接收方要能收到，需要在 Manifest 里声明 [NotificationReceiver]（见产出补丁）；
     *   没有声明时点击通知会没有任何反应 —— 这也是本波明确写出来的接线缺口，而不是静默。
     */
    private fun clickIntent(ctx: Context, id: String): PendingIntent {
        val intent = Intent(ctx, NotificationReceiver::class.java)
            .setAction(ACTION_NOTIFICATION_CLICK)
            .setData(Uri.parse("mygo://notification/${Uri.encode(id)}"))
            .putExtra(EXTRA_NOTIFICATION_ID, id)
        return PendingIntent.getBroadcast(
            ctx,
            0,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
    }

    /**
     * 通知被点击时的接收方。
     *
     * 用 BroadcastReceiver 而不是 Activity：Activity 没在运行时（应用被系统回收后）点击
     * 通知也会走到这里，Go 侧的 AppHandler.NotificationClicked 因此不会漏。
     * 需要在 Manifest 里注册（`android:exported="false"`，只由本应用的通知触发）。
     */
    class NotificationReceiver : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            if (intent.action != ACTION_NOTIFICATION_CLICK) return
            val id = intent.getStringExtra(EXTRA_NOTIFICATION_ID) ?: return
            val nm = context.getSystemService(Context.NOTIFICATION_SERVICE) as? NotificationManager
            nm?.cancel(id, NOTIFICATION_ID)
            // Go 侧：AppHandler.NotificationClicked(id)。
            MygoPlatform.notificationClicked(id)
        }
    }

    /** 通知被点击：把应用给的 id（UTF-8 字节）交给 Go 侧。 */
    fun notificationClicked(id: String) {
        nativeNotificationClicked(id.toByteArray(Charsets.UTF_8))
    }

    /** 无障碍动作：把动作名（UTF-8 字节）交给 Go 侧。 */
    fun accessibilityAction(id: Long, action: String) {
        nativeAccessibilityAction(id, action.toByteArray(Charsets.UTF_8))
    }

    // ─────────────────────────── 无障碍 ───────────────────────────

    /**
     * AccessibilityManager：判断无障碍服务是否开启。TalkBack 没开时整套 provider 都不会
     * 被调用，Go 侧也就没必要把树送过来（送也不会有人读）。
     */
    private fun accessibilityEnabled(ctx: Context): Boolean {
        val am = ctx.getSystemService(Context.ACCESSIBILITY_SERVICE) as? AccessibilityManager
        return am?.isEnabled == true
    }

    /**
     * 把 Go 侧序列化好的 AccessTree（见 internal/android/backend/platform.go 的
     * encodeAccessTree）交给 Java 侧保存，供 provider 服务。
     *
     * 🔴 **为什么必须走 provider**（Android 官方硬要求）：像本工程这样用自绘 SurfaceView
     * 呈现内容的应用，View 里没有任何子 View，TalkBack 读到的就是「一个空白 View」——
     * 完全不可用。必须 override getAccessibilityNodeProvider() 或使用
     * ExploreByTouchHelper 把自绘内容暴露成虚拟节点，否则无障碍是彻底失效的。
     *
     * 本实现用**框架自带的** AccessibilityNodeProvider（不依赖 AndroidX，因此
     * **不需要**动 build.gradle 加 ExploreByTouchHelper 依赖）。
     */
    fun setAccessibilityTree(json: ByteArray) {
        val tree = AccessTree.parse(String(json, Charsets.UTF_8)) ?: return
        val ctx = activity ?: return
        if (!accessibilityEnabled(ctx)) return
        nodes = tree
        onUi {
            val view = host ?: return@onUi
            // 内容变了：让 TalkBack 重新取节点。类型用 WINDOW_CONTENT_CHANGED，
            // 这是自绘内容更新时的正确事件。
            view.sendAccessibilityEvent(AccessibilityEvent.TYPE_WINDOW_CONTENT_CHANGED)
        }
    }

    /** TalkBack 要求朗读一段文字（announcement），读在它当前朗读内容之后。 */
    fun announce(text: ByteArray) {
        val message = String(text, Charsets.UTF_8)
        if (message.isEmpty()) return
        onUi { host?.announceForAccessibility(message) }
    }

    /** 当前无障碍树；@Volatile 因为写来自 Go 的线程、读来自 UI/无障碍线程。 */
    @Volatile
    private var nodes: AccessTree? = null

    /** 无障碍焦点（虚拟节点 id），TalkBack 移动焦点时更新。 */
    @Volatile
    private var accessibilityFocusId: Int = View.NO_ID

    private fun clearAccessibilityTree() {
        nodes = null
        accessibilityFocusId = View.NO_ID
        MygoPlatform.virtualIds.reset()
    }

    /**
     * 挂在宿主 View 上的 AccessibilityDelegate：提供虚拟节点。
     *
     * 这是「自绘 View 对 TalkBack 可用」的入口点。用 View.AccessibilityDelegate 而不是让
     * MygoView 直接 override：MygoView 不归本条线（也不该知道无障碍的细节），
     * `view.accessibilityDelegate = …` 一行就能装上。
     */
    private class MygoAccessibilityDelegate : View.AccessibilityDelegate() {
        override fun getAccessibilityNodeProvider(host: View): AccessibilityNodeProvider =
            MygoNodeProvider()
    }

    /**
     * 虚拟节点 id 的分配：Go 侧的节点 id 是 uint64 且跨帧稳定，而 provider 的虚拟 id 是
     * int 且只需在本进程内唯一。这里做一次映射并**永不复用**（TalkBack 会跨帧持有 id，
     * 复用会让它认错节点）；映射表在进程内缓慢增长，一屏内容的节点数是有界的。
     */
    private object virtualIds {
        private val toVirtual = HashMap<Long, Int>()
        private val toNative = HashMap<Int, Long>()
        private var next = 1

        @Synchronized
        fun of(id: Long): Int {
            toVirtual[id]?.let { return it }
            val v = next++
            toVirtual[id] = v
            toNative[v] = id
            return v
        }

        @Synchronized
        fun native(virtual: Int): Long? = toNative[virtual]

        @Synchronized
        fun reset() {
            toVirtual.clear()
            toNative.clear()
            next = 1
        }
    }

    /**
     * AccessibilityNodeProvider：把 JSON 树变成 AccessibilityNodeInfo。
     *
     * 覆盖范围（**如实**，不夸大）：
     *  · 有：source/parent/children、className（按 role 映射）、contentDescription
     *    （label，另把 description 接在后面）、text（value）、hintText（placeholder）、
     *    boundsInParent/InScreen（DIP→px 且加上宿主 View 在屏幕上的位置）、
     *    enabled/disabled、focusable、checkable/checked、selected、scrollable、
     *    editable/password/multiLine、heading、collectionItemInfo（posInSet/setSize）、
     *    rangeInfo（slider/progress/meter/stepper）、clickable 与点击/焦点/前后滚动动作。
     *  · 无（本波未做，写在这里以免被当成「已支持」）：文字选区（selStart/selEnd）、
     *    ACTION_SET_PROGRESS 的值回传（wire 上没有携带值的通道）、
     *    表格的行列跨度（RoleTable 的 rowSpan 之类，平台契约里也没有）、
     *    以及**任何点击/动作的实际效果** —— 动作已经送到 Go 侧，但 Go 侧没有消费者
     *    （见 internal/android/backend/platform.go 的 SetAccessActionSink 说明）。
     */
    private class MygoNodeProvider : AccessibilityNodeProvider() {

        override fun createAccessibilityNodeInfo(virtualViewId: Int): AccessibilityNodeInfo? {
            val host = MygoPlatform.host ?: return null
            val tree = MygoPlatform.nodes ?: return null

            if (virtualViewId == HOST_VIEW_ID) {
                // 宿主节点本身：包住所有顶层节点。没有它，TalkBack 找不到入口。
                val info = AccessibilityNodeInfo.obtain(host)
                host.onInitializeAccessibilityNodeInfo(info)
                for (node in tree.roots()) {
                    info.addChild(host, MygoPlatform.virtualIds.of(node.id))
                }
                return info
            }

            val nativeId = MygoPlatform.virtualIds.native(virtualViewId) ?: return null
            val node = tree.node(nativeId) ?: return null

            val info = AccessibilityNodeInfo.obtain()
            info.packageName = host.context.packageName
            info.setSource(host, virtualViewId)
            // 顶层节点挂在宿主上；其余挂在各自的父节点上。Go 侧的 Parent 是**下标**
            // （-1 = 顶层），不是 id，所以这里要下标→id 转一次。
            val parentId = tree.parentIdOf(node)
            if (parentId == null) {
                info.setParent(host)
            } else {
                info.setParent(host, MygoPlatform.virtualIds.of(parentId))
            }
            for (childId in tree.childIdsOf(node)) {
                info.addChild(host, MygoPlatform.virtualIds.of(childId))
            }

            info.className = roleClassName(node.role)
            val description = if (node.description.isEmpty()) node.label else "${node.label}, ${node.description}"
            if (description.isNotEmpty()) info.contentDescription = description
            if (node.value.isNotEmpty()) info.text = node.value
            if (node.placeholder.isNotEmpty()) info.hintText = node.placeholder

            val scale = host.resources.displayMetrics.density
            val location = IntArray(2)
            host.getLocationOnScreen(location)
            val bounds = Rect(
                location[0] + (node.bounds.left * scale).toInt(),
                location[1] + (node.bounds.top * scale).toInt(),
                location[0] + (node.bounds.right * scale).toInt(),
                location[1] + (node.bounds.bottom * scale).toInt()
            )
            info.setBoundsInParent(
                Rect(
                    (node.bounds.left * scale).toInt(),
                    (node.bounds.top * scale).toInt(),
                    (node.bounds.right * scale).toInt(),
                    (node.bounds.bottom * scale).toInt()
                )
            )
            info.setBoundsInScreen(bounds)

            info.isEnabled = !node.states.contains("disabled")
            info.isFocusable = node.states.contains("focusable") || node.actions.contains("focus")
            info.isFocused = tree.focus == node.id
            info.isCheckable = node.role == "checkbox" || node.role == "radio" || node.role == "switch" ||
                node.role == "togglebutton" || node.role == "menuitemcheckbox" || node.role == "menuitemradio"
            info.isChecked = node.states.contains("checked")
            info.isSelected = node.states.contains("selected")
            info.isScrollable = node.role == "scroll" || node.role == "list" || node.role == "table"
            info.isEditable = node.role == "textfield" || node.role == "combobox"
            info.isPassword = node.states.contains("password")
            info.isMultiLine = node.states.contains("multiline")
            info.isHeading = node.role == "heading"
            info.isVisibleToUser = !node.states.contains("offscreen")
            info.isAccessibilityFocused = MygoPlatform.accessibilityFocusId == virtualViewId
            info.isClickable = node.actions.contains("press")
            info.isLongClickable = false

            if (node.posInSet > 0 || node.setSize > 0) {
                info.setCollectionItemInfo(
                    AccessibilityNodeInfo.CollectionItemInfo.obtain(
                        /* rowIndex = */ node.posInSet - 1,
                        /* rowSpan = */ 1,
                        /* columnIndex = */ 0,
                        /* columnSpan = */ 1,
                        info.isHeading
                    )
                )
            }
            if (isRanged(node.role)) {
                info.rangeInfo = AccessibilityNodeInfo.RangeInfo.obtain(
                    AccessibilityNodeInfo.RangeInfo.RANGE_TYPE_FLOAT,
                    node.min.toFloat(),
                    node.max.toFloat(),
                    node.now.toFloat()
                )
            }
            if (node.actions.contains("press")) info.addAction(AccessibilityNodeInfo.ACTION_CLICK)
            if (node.actions.contains("focus")) info.addAction(AccessibilityNodeInfo.ACTION_FOCUS)
            if (node.actions.contains("increment")) info.addAction(AccessibilityNodeInfo.ACTION_SCROLL_FORWARD)
            if (node.actions.contains("decrement")) info.addAction(AccessibilityNodeInfo.ACTION_SCROLL_BACKWARD)
            return info
        }

        /**
         * TalkBack 对某个虚拟节点执行动作：转成 Go 侧的 action 名字回传。
         *
         * 名字必须与 internal/android/backend/platform.go 的 accessActionKinds 一致
         * （press / focus / increment / decrement / setvalue / scrollintoview / expand /
         * collapse），对不上 Go 侧会拒绝并记 logcat。
         */
        override fun performAction(
            virtualViewId: Int,
            action: Int,
            arguments: Bundle?
        ): Boolean {
            val host = MygoPlatform.host ?: return false
            if (virtualViewId != HOST_VIEW_ID && MygoPlatform.virtualIds.native(virtualViewId) == null) return false
            val name = when (action) {
                AccessibilityNodeInfo.ACTION_CLICK -> "press"
                AccessibilityNodeInfo.ACTION_FOCUS,
                AccessibilityNodeInfo.ACTION_ACCESSIBILITY_FOCUS -> "focus"
                AccessibilityNodeInfo.ACTION_SCROLL_FORWARD -> "increment"
                AccessibilityNodeInfo.ACTION_SCROLL_BACKWARD -> "decrement"
                else -> return false
            }
            if (action == AccessibilityNodeInfo.ACTION_ACCESSIBILITY_FOCUS) {
                // 无障碍焦点是 Java 侧的状态：TalkBack 移动焦点时要给它 FOCUSED 事件，
                // 否则它不知道自己「到了哪里」。清除旧焦点是协议要求的一半。
                sendFocusEvent(host, virtualViewId, accessibilityFocused = true)
            }
            val nativeId = MygoPlatform.virtualIds.native(virtualViewId)
            // 宿主节点上的动作（例如 CLICK）没有对应元素，不往 Go 侧送。
            if (nativeId != null) {
                MygoPlatform.accessibilityAction(nativeId, name)
            }
            return true
        }

        override fun findAccessibilityNodeInfosByText(
            text: String,
            virtualViewId: Int
        ): MutableList<AccessibilityNodeInfo> {
            val out = ArrayList<AccessibilityNodeInfo>()
            val tree = MygoPlatform.nodes ?: return out
            val needle = text.lowercase()
            for (node in tree.nodes) {
                if (node.label.lowercase().contains(needle) || node.value.lowercase().contains(needle)) {
                    createAccessibilityNodeInfo(MygoPlatform.virtualIds.of(node.id))?.let { out.add(it) }
                }
            }
            return out
        }

        /** 发焦点事件：进入/离开某个虚拟节点。 */
        private fun sendFocusEvent(host: View, virtualViewId: Int, accessibilityFocused: Boolean) {
            val type = if (accessibilityFocused) {
                AccessibilityEvent.TYPE_VIEW_ACCESSIBILITY_FOCUSED
            } else {
                AccessibilityEvent.TYPE_VIEW_ACCESSIBILITY_FOCUS_CLEARED
            }
            MygoPlatform.accessibilityFocusId = if (accessibilityFocused) virtualViewId else View.NO_ID
            val event = AccessibilityEvent.obtain(type)
            event.packageName = host.context.packageName
            event.className = MygoPlatform::class.java.name
            event.setSource(host, virtualViewId)
            host.parent?.requestSendAccessibilityEvent(host, event)
        }
    }

    /** ranged role：值在区间里，能变成 RangeInfo。 */
    private fun isRanged(role: String): Boolean =
        role == "slider" || role == "progress" || role == "meter" || role == "stepper"

    /**
     * role → Android 类名。TalkBack 靠类名决定「这是个按钮还是个文本」，也靠它选朗读措辞。
     * Go 侧没给的 role（"" 或未知）落到 View，即「普通元素」，而不是随便挑一个看起来像的。
     */
    private fun roleClassName(role: String): String = when (role) {
        "button", "togglebutton", "disclosure", "colorwell", "popupbutton", "menubutton" -> "android.widget.Button"
        "checkbox", "menuitemcheckbox" -> "android.widget.CheckBox"
        "radio", "menuitemradio" -> "android.widget.RadioButton"
        "switch" -> "android.widget.Switch"
        "text" -> "android.widget.TextView"
        "heading" -> "android.view.View"
        "link" -> "android.widget.TextView"
        "image" -> "android.widget.ImageView"
        "textfield", "search", "combobox" -> "android.widget.EditText"
        "progress", "meter" -> "android.widget.ProgressBar"
        "slider", "stepper" -> "android.widget.SeekBar"
        "list", "listitem", "grid" -> "android.widget.ListView"
        "scroll" -> "android.widget.ScrollView"
        "tab", "tablist" -> "android.widget.TabWidget"
        "menu", "menubar", "menuitem", "popup" -> "android.widget.PopupMenu"
        "toolbar" -> "android.widget.Toolbar"
        "table" -> "android.widget.TableLayout"
        "row", "cell", "columnheader" -> "android.widget.TextView"
        "tree", "treeitem" -> "android.widget.ListView"
        "dialog", "alertdialog", "tooltip" -> "android.app.Dialog"
        "status" -> "android.widget.TextView"
        "splitter" -> "android.widget.SeekBar"
        "radiogroup", "group" -> "android.view.ViewGroup"
        else -> "android.view.View"
    }

    /**
     * 无障碍树的 Java 侧副本：从 Go 侧的 JSON 解析出来。
     *
     * 形状见 internal/android/backend/platform.go 的 accessTreeJSON / accessNodeJSON
     * （`{"focus":N,"nodes":[…],"announcements":[…]}`，节点带 id/parent(下标)/role/label/
     * value/bounds[4]/states[]/actions[]/数值字段）。解析是**宽容的**：缺字段取默认值，
     * 未知 role/states/actions 原样保留为字符串（约定里 Java 侧只认自己知道的名字）。
     */
    private class AccessTree(
        val focus: Long,
        val nodes: List<Node>
    ) {
        /** 下标 → 节点，用于 parent（Go 侧是下标）与 children（自己算）。 */
        fun node(id: Long): Node? = nodes.firstOrNull { it.id == id }

        fun roots(): List<Node> = nodes.filter { it.parent < 0 || it.parent >= nodes.size }

        fun parentIdOf(node: Node): Long? =
            if (node.parent in nodes.indices) nodes[node.parent].id else null

        fun childIdsOf(node: Node): List<Long> {
            val index = nodes.indexOfFirst { it.id == node.id }
            if (index < 0) return emptyList()
            return nodes.filter { it.parent == index }.map { it.id }
        }

        companion object {
            fun parse(json: String): AccessTree? = try {
                val root = JSONObject(json)
                val array = root.optJSONArray("nodes") ?: JSONArray()
                val nodes = ArrayList<Node>(array.length())
                for (i in 0 until array.length()) {
                    val o = array.optJSONObject(i) ?: continue
                    nodes.add(Node.parse(o))
                }
                AccessTree(root.optLong("focus", 0L), nodes)
            } catch (e: Exception) {
                // 畸形 JSON 直接放弃这次更新（保持上一帧的树）：宁可少更新，也不要
                // 在无障碍线程里抛异常。
                null
            }
        }
    }

    /** 一个节点；字段与 Go 侧 accessNodeJSON 一一对应（bounds 单位是 DIP，相对 surface）。 */
    private class Node(
        val id: Long,
        val parent: Int,
        val role: String,
        val label: String,
        val value: String,
        val description: String,
        val placeholder: String,
        val bounds: Rect,
        val states: Set<String>,
        val actions: Set<String>,
        val min: Double,
        val max: Double,
        val now: Double,
        val step: Double,
        val level: Int,
        val posInSet: Int,
        val setSize: Int,
        val selStart: Int,
        val selEnd: Int
    ) {
        companion object {
            fun parse(o: JSONObject): Node {
                val b = o.optJSONArray("bounds")
                val bounds = Rect(
                    b?.optDouble(0, 0.0)?.toInt() ?: 0,
                    b?.optDouble(1, 0.0)?.toInt() ?: 0,
                    (b?.optDouble(0, 0.0) ?: 0.0).toInt() + (b?.optDouble(2, 0.0) ?: 0.0).toInt(),
                    (b?.optDouble(1, 0.0) ?: 0.0).toInt() + (b?.optDouble(3, 0.0) ?: 0.0).toInt()
                )
                return Node(
                    id = o.optLong("id", 0L),
                    parent = o.optInt("parent", -1),
                    role = o.optString("role", ""),
                    label = o.optString("label", ""),
                    value = o.optString("value", ""),
                    description = o.optString("description", ""),
                    placeholder = o.optString("placeholder", ""),
                    bounds = bounds,
                    states = o.optJSONArray("states").toStringSet(),
                    actions = o.optJSONArray("actions").toStringSet(),
                    min = o.optDouble("min", 0.0),
                    max = o.optDouble("max", 0.0),
                    now = o.optDouble("now", 0.0),
                    step = o.optDouble("step", 0.0),
                    level = o.optInt("level", 0),
                    posInSet = o.optInt("posInSet", 0),
                    setSize = o.optInt("setSize", 0),
                    selStart = o.optInt("selStart", 0),
                    selEnd = o.optInt("selEnd", 0)
                )
            }
        }
    }

    private fun JSONArray?.toStringSet(): Set<String> {
        if (this == null) return emptySet()
        val out = HashSet<String>(length())
        for (i in 0 until length()) {
            val s = optString(i, "")
            if (s.isNotEmpty()) out.add(s)
        }
        return out
    }

    // ─────────────────────────── 线程与 JNI 契约 ───────────────────────────

    /** 在主线程执行 block：已在主线程就直接跑，否则 post。 */
    private fun onUi(block: () -> Unit) {
        if (Looper.myLooper() == Looper.getMainLooper()) {
            block()
        } else {
            mainHandler.post(block)
        }
    }

    /**
     * Android 13+ 的通知权限常量。
     *
     * 单独包一层是为了在 API < 33 的**运行**设备上也安全：直接引用
     * `Manifest.permission.POST_NOTIFICATIONS` 在编译期没问题（compileSdk=36 有这个字段），
     * 但常量值在旧系统上无意义，包一层会让「只在 33+ 读它」这件事显式。
     */
    private fun Manifest_permission_POST_NOTIFICATIONS(): String =
        "android.permission.POST_NOTIFICATIONS"

    private const val SCAN_REQUEST = 0x5CA9
    private const val SCAN_OK = 0
    private const val SCAN_CANCELED = 5
    private const val SCAN_NOT_FOUND = 2
    private const val KEY_ALIAS = "mygo-secrets-v1"
    private const val GCM_IV_LEN = 12
    private const val GCM_TAG_BITS = 128

    // ── Java → Go：3 个 external（JNI 符号 Java_org_mygo_MygoPlatform_*）──
    // 名字/参数/返回值与 android/jni/jni_platform.go 的 //export **逐字**一致，
    // 改一个就两边一起改（javap 是核对手段，见产出）。

    /** 记下 JavaVM 与 MygoPlatform 对象，让 Go 侧可以回调本文件的普通方法。 */
    private external fun nativeAttach()

    /** 通知被点击（参数是应用给的通知 id 的 UTF-8 字节）。 */
    private external fun nativeNotificationClicked(id: ByteArray)

    /** 无障碍动作（参数是动作名字的 UTF-8 字节，名字见 accessActionKinds）。 */
    private external fun nativeAccessibilityAction(id: Long, action: ByteArray)

    /** 扫码结果回传：token 对应 Go 侧的请求，code 见 ScanResult 的注释。 */

    // ── 扫码（系统相机/相册 + 解码）─────────────────────────────────────
    //
    // 不做自己的相机预览：用系统相机拍一张、或用相册里已有的照片，再解码。
    // 好处是**不需要 CAMERA 权限**（拍照的是相机应用，权限归它）、没有预览
    // 层的生命周期要管、也不用把相机栈编进来。
    //
    // 解码用 ZXing core（纯 Java，无 NDK）。它不依赖任何 UI，也不申请权限。

    /**
     * 把扫码结果存起来，等 Go 侧来取。
     *
     * 🔴 这套 bridge 是**单向**的：Go 用 jmethodID 调 Java，Java 从不回头调 Go。
     * 所以结果不能「推」过去，只能存下、由 Go 侧的 collectScanResults 轮询
     * takeScanResult 拿走。一开始我按 //export 写了推送，那个符号根本没注册，
     * 传过去的字符串也就成了垃圾。
     *
     * 编码格式："<code>\n<token>\n<text>"，与 Go 侧的 takeScanResult 对应。
     */
    private var pendingResult: String? = null

    private fun deliverScanResult(token: Long, text: String?, code: Int) {
        pendingResult = "$code\n$token\n" + (text ?: "")
        android.util.Log.i("mygo", "scan: deliver code=$code len=${text?.length ?: 0}")
    }

    /** Go 侧轮询调用：取走待发的结果，没有就返回 null。 */
    fun takeScanResult(): String? = pendingResult.also { pendingResult = null }

    private var scanToken: Long = 0
    private var pendingScanToken: Long = 0
    /** 相机要写的那个文件；onScanResult 读完就删。 */
    private var scanFile: java.io.File? = null

    /**
     * 打开系统相机拍一张。返回 false 只在连 Intent 都起不来时（没有相机应用）。
     *
     * 🔴 **不要用 `resolveActivity` 判断有没有相机。** Android 11 起包可见性
     * 会把没声明在 `<queries>` 里的包过滤掉，`resolveActivity` 就返回 null——
     * 设备上有相机也一样。这里按官方写法直接 try/catch 启动，`ActivityNotFound`
     * 才是「真没有」。Manifest 里同时声明了 queries，两者一起才可靠。
     *
     * 结果异步回到 [onScanResult]，那时才调 deliverScanResult。
     */
    fun startScan(token: Long, prompt: ByteArray): Boolean {
        val ctx = activity ?: return false
        pendingScanToken = token
        scanToken = token
        // 先开相册，不开相机。
        //
        // 理由：相册里的图是**已经拍好的、清晰的、静止的**，解出来的成功率远高于
        // 现场手持拍屏幕——实测手持拍一张，对焦稍偏 OpenCV 就解不出。桌面上
        // 显示二维码时，用系统相机拍一张再回来选，是更稳的路子；用户也能直接
        // 截屏后选那张截图。
        //
        // 相册也不行时（没有照片选择器），才退回相机。
        return try {
            ctx.startActivityForResult(
                Intent(MediaStore.ACTION_PICK_IMAGES).apply { type = "image/*" },
                SCAN_REQUEST,
            )
            android.util.Log.i("mygo", "scan: photo picker up")
            true
        } catch (e: Exception) {
            android.util.Log.w("mygo", "scan: no photo picker, trying the camera: $e")
            tryCamera(ctx)
        }
    }

    /** 拍照。相册不可用时才走这里。 */
    private fun tryCamera(ctx: Activity): Boolean = try {
        // 🔴 EXTRA_OUTPUT 必须有。没有它，ACTION_IMAGE_CAPTURE 回来的只是
        // extras["data"] 里的一张**缩略图**（约 100~200px），一张缩略图里的
        // 二维码基本解不出来。
        //
        // 🔴 路径必须是 externalCacheDir，不能用 cacheDir。私有缓存
        // （/data/data/<pkg>/cache）相机应用没有写权限，一部分厂商相机会
        // 默默写失败、然后返回 RESULT_CANCELED。
        val dir = ctx.externalCacheDir ?: ctx.cacheDir
        dir.mkdirs()
        val file = java.io.File(dir, "scan-${pendingScanToken}.jpg")
        if (file.exists()) file.delete()
        val uri = androidx.core.content.FileProvider.getUriForFile(
            ctx, "${ctx.packageName}.fileprovider", file,
        )
        scanFile = file
        ctx.startActivityForResult(
            Intent(MediaStore.ACTION_IMAGE_CAPTURE)
                .putExtra(MediaStore.EXTRA_OUTPUT, uri)
                .addFlags(Intent.FLAG_GRANT_WRITE_URI_PERMISSION),
            SCAN_REQUEST,
        )
        android.util.Log.i("mygo", "scan: camera up, output=$uri")
        true
    } catch (e: Exception) {
        android.util.Log.w("mygo", "scan: could not start the camera: $e")
        pendingScanToken = 0
        false
    }

    /** 取景界面被收起，但没有结果（用户返回）。 */
    fun cancelScan(token: Long): Boolean {
        if (pendingScanToken != token) return false
        pendingScanToken = 0
        onUi { deliverScanResult(token, null, SCAN_CANCELED) }
        return true
    }

    /**
     * Activity 的 onActivityResult 转发进来。
     *
     * 解码在后台线程做：一张 4000×3000 的照片解码要几十到几百毫秒，放在主线程
     * 会卡住界面。
     */
    fun onScanResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (requestCode != SCAN_REQUEST) {
            android.util.Log.i("mygo", "scan: ignoring result for code $requestCode")
            return
        }
        val token = pendingScanToken
        pendingScanToken = 0
        if (token == 0L) {
            android.util.Log.w("mygo", "scan: result with no pending token")
            return
        }
        val ctx = activity
        android.util.Log.i(
            "mygo", "scan: result token=$token code=$resultCode data=${data != null} file=${scanFile?.length() ?: -1}",
        )
        if (ctx == null) {
            deliverScanResult(token, null, SCAN_CANCELED)
            return
        }
        // 🔴 不看 resultCode 就判取消，是错的。厂商相机（ColorOS 实测）会用
        // EXTRA_OUTPUT 把照片写进我们给的 Uri，然后**仍然返回 RESULT_CANCELED**
        // ——文件好好的，结果码是取消。它以「文件里有没有东西」为准：
        // 有照片就去解，解不出才是「没找到码」；只有文件也没写时才算取消。
        val wrote = scanFile?.let { it.exists() && it.length() > 0 } ?: false
        // 相册回来时照片在 data.data 的 Uri 里，没有文件、可能也没有 OK：
        // 只要有「能解的东西」就不当取消。
        val haveImage = wrote || data?.data != null || data?.extras?.get("data") != null
        if (resultCode != Activity.RESULT_OK && !haveImage) {
            android.util.Log.i("mygo", "scan: canceled, no file written")
            deliverScanResult(token, null, SCAN_CANCELED)
            return
        }
        if (resultCode != Activity.RESULT_OK) {
            android.util.Log.i("mygo", "scan: resultCode=$resultCode but there is an image; decoding it")
        }
        // 三处来源，按「能解出码」的可能性排序：
        //   1. EXTRA_OUTPUT 写的那个文件 —— 全尺寸，首选
        //   2. data.extras["data"] —— 缩略图，只在没给 EXTRA_OUTPUT 时出现
        //   3. data.data 的 Uri —— 相册选的图
        @Suppress("DEPRECATION")
        val thumb = data?.extras?.get("data") as? android.graphics.Bitmap
        val uri = data?.data
        val file = scanFile
        Thread {
            val text = try {
                val bitmap = when {
                    file != null && file.length() > 0 -> android.graphics.BitmapFactory.decodeFile(file.path)
                    thumb != null -> thumb
                    uri != null -> MediaStore.Images.Media.getBitmap(ctx.contentResolver, uri)
                    else -> null
                }
                android.util.Log.i(
                    "mygo", "scan: decoded bitmap=" + (bitmap?.let { "${it.width}x${it.height}" } ?: "null"),
                )
                bitmap?.let { decodeQr(it) }
            } catch (e: Exception) {
                android.util.Log.w("mygo", "scan: decode threw: $e")
                null
            } catch (e: OutOfMemoryError) {
                android.util.Log.w("mygo", "scan: bitmap too large: $e")
                null
            }
            file?.delete()
            android.util.Log.i("mygo", "scan: text=" + (text?.take(60) ?: "null"))
            onUi {
                if (text.isNullOrEmpty()) {
                    deliverScanResult(token, null, SCAN_NOT_FOUND)
                } else {
                    deliverScanResult(token, text, SCAN_OK)
                }
            }
        }.start()
    }

    /** 用 ZXing 在一张位图里找 QR 码；找不到返回 null。 */
    private fun decodeQr(bitmap: android.graphics.Bitmap): String? {
        // ZXing core，不引 OpenCV。
        //
        // 之前一度换成 OpenCV，理由是「同一张照片 ZXing 解不出、OpenCV 能」。
        // 复测后那个结论站不住：把四个解码器放到同一批图上对照，
        // · 相册预览页那张（p5）：OpenCV 0 命中，ZXing 命中
        // · 其余清晰图：两者都命中
        // 再对规范码做 9 种劣化（模糊、运动模糊、低对比、暗光、旋转 8°、
        // 透视倾斜、JPEG q25、半尺寸、原图），两者全部通过。
        // 而 OpenCV 的 .so 是 21.6MB（APK 的三成），ZXing 的 jar 是 0.58MB。
        // 没有证据支持多付这 21MB，所以换回来。
        //
        // 下面两步仍然必要：
        //   1. 转灰度 —— RGBLuminanceSource 取的是 R 通道，不是真亮度。
        //      拍彩色屏幕时 R 通道几乎没对比度，二值化会毁掉。
        //   2. 缩到 2000px 以内 —— ZXing 逐像素扫，4096 的边没有额外收益。
        val gray = toGray(bitmap)
        val scaled = scaleDown(gray, 2000)
        val w = scaled.width
        val h = scaled.height
        val pixels = IntArray(w * h)
        scaled.getPixels(pixels, 0, w, 0, 0, w, h)
        for (i in pixels.indices) {
            val g = pixels[i] and 0xFF
            pixels[i] = (0xFF shl 24) or (g shl 16) or (g shl 8) or g
        }
        return try {
            val hints = java.util.EnumMap<com.google.zxing.DecodeHintType, Any>(com.google.zxing.DecodeHintType::class.java)
            // 试一遍再试一遍：第一次失败时 TRY_HARDER 会把旋转与更细的二值化也走一遍。
            hints[com.google.zxing.DecodeHintType.TRY_HARDER] = true
            val reader = com.google.zxing.MultiFormatReader().apply { setHints(hints) }
            reader.decode(com.google.zxing.BinaryBitmap(
                com.google.zxing.common.HybridBinarizer(
                    com.google.zxing.RGBLuminanceSource(w, h, pixels),
                ),
            )).text
        } catch (e: com.google.zxing.NotFoundException) {
            android.util.Log.i("mygo", "scan: ZXing found no code in ${w}x$h")
            null
        } catch (e: Exception) {
            android.util.Log.w("mygo", "scan: ZXing threw: $e")
            null
        }
    }

    /** 按 (0.299, 0.587, 0.114) 转灰度，与 OpenCV/PhotoShop 同公式。 */
    private fun toGray(src: android.graphics.Bitmap): android.graphics.Bitmap {
        val out = android.graphics.Bitmap.createBitmap(src.width, src.height, android.graphics.Bitmap.Config.ARGB_8888)
        val canvas = android.graphics.Canvas(out)
        val paint = android.graphics.Paint().apply {
            colorFilter = android.graphics.ColorMatrixColorFilter(
                android.graphics.ColorMatrix(
                    floatArrayOf(
                        0.299f, 0.587f, 0.114f, 0f, 0f,
                        0.299f, 0.587f, 0.114f, 0f, 0f,
                        0.299f, 0.587f, 0.114f, 0f, 0f,
                        0f, 0f, 0f, 1f, 0f,
                    )
                )
            )
        }
        canvas.drawBitmap(src, 0f, 0f, paint)
        return out
    }

    private fun scaleDown(src: android.graphics.Bitmap, maxSide: Int): android.graphics.Bitmap {
        val longest = maxOf(src.width, src.height)
        if (longest <= maxSide) return src
        val k = maxSide.toFloat() / longest
        return android.graphics.Bitmap.createScaledBitmap(
            src, (src.width * k).toInt(), (src.height * k).toInt(), true,
        )
    }

    // ── 安全存储（Keystore）─────────────────────────────────────────────
    //
    // 一个本应用的 AES 密钥放在 AndroidKeyStore 里（设备有硬件就落硬件），
    // 条目用它是 GCM 加密后 Base64 存进本应用的 SharedPreferences。这是 Android
    // 给得到的东西，也是为什么 Get 会在 Set 成功过之后仍然失败：删掉锁屏、或把
    // 应用数据恢复到另一台设备，密钥会被作废，它加密过的条目就读不回来了。
    // 那时这里返回 null，Go 侧报 platform.ErrSecretNotFound，并把原因记进 logcat。
    //
    // 与 iOS 的 Keychain 相比少了「按条目设生命周期」：android 的条目设备解锁后
    // 就能读，所以 accessibility 参数只区分「首次解锁后可用」，其余取值等价。

    private fun secretPrefs(ctx: Context) =
        ctx.getSharedPreferences("mygo-secrets", Context.MODE_PRIVATE)

    private fun secretKey(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getEntry(KEY_ALIAS, null) as? KeyStore.SecretKeyEntry)?.let { return it.secretKey }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build()
        )
        return gen.generateKey()
    }

    private fun secretEntryName(ns: String, key: String): String =
        Base64.encodeToString("$ns\u0000$key".toByteArray(Charsets.UTF_8), Base64.NO_WRAP)

    /**
     * 存一个值。返回 true 表示已写入。
     *
     * `accessibility` 目前只用来占位：Keystore 的条目没有按条目的生命周期。
     */
    fun secretSet(ns: ByteArray, key: ByteArray, value: ByteArray, accessibility: Int): Boolean {
        val ctx = activity ?: return false
        return try {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, secretKey())
            val sealed = cipher.iv + cipher.doFinal(value)
            secretPrefs(ctx).edit()
                .putString(
                    secretEntryName(String(ns, Charsets.UTF_8), String(key, Charsets.UTF_8)),
                    Base64.encodeToString(sealed, Base64.NO_WRAP),
                )
                .apply()
            true
        } catch (e: Exception) {
            android.util.Log.w("mygo", "secretSet failed: $e")
            false
        }
    }

    /** 读一个值；不存在或密钥已作废时返回 null。 */
    fun secretGet(ns: ByteArray, key: ByteArray): ByteArray? {
        val ctx = activity ?: return null
        val name = secretEntryName(String(ns, Charsets.UTF_8), String(key, Charsets.UTF_8))
        val stored = secretPrefs(ctx).getString(name, null) ?: return null
        return try {
            val sealed = Base64.decode(stored, Base64.NO_WRAP)
            if (sealed.size <= GCM_IV_LEN) return null
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(
                Cipher.DECRYPT_MODE,
                secretKey(),
                GCMParameterSpec(GCM_TAG_BITS, sealed, 0, GCM_IV_LEN),
            )
            cipher.doFinal(sealed, GCM_IV_LEN, sealed.size - GCM_IV_LEN)
        } catch (e: Exception) {
            // A key that was invalidated, or a value from another install:
            // unreadable, and worth saying so.
            android.util.Log.w("mygo", "secretGet failed: $e")
            null
        }
    }

    /** 删一个值；不存在也算成功。 */
    fun secretDelete(ns: ByteArray, key: ByteArray): Boolean {
        val ctx = activity ?: return false
        return try {
            secretPrefs(ctx).edit()
                .remove(secretEntryName(String(ns, Charsets.UTF_8), String(key, Charsets.UTF_8)))
                .commit()
        } catch (e: Exception) {
            android.util.Log.w("mygo", "secretDelete failed: $e")
            false
        }
    }
}
