package org.mygo

import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.view.Window
import android.view.WindowManager

/**
 * 宿主 Activity：只做「挂一个 MygoView 并保持前台」。
 *
 * 设计要点：
 *  · 不在 onDestroy 里通知 Go 侧释放 —— Go 侧保活（见 MygoView 文件头注释 #1）。
 *    语义上：「Activity 没了」不等于「Go 侧渲染上下文没了」。
 *  · 不做任何会改动 SurfaceView z-order 语义的层级加工（保持 setContentView(view) 单层）。
 *  · **系统栏显示**（状态栏 + 手势条），内容铺到屏幕边缘再由 Go 侧躲开 ——
 *    主题见 AndroidManifest.xml 的 MygoTheme，躲开的机制见 MygoView.reportSafeArea。
 *  · 屏幕方向策略见 AndroidManifest.xml 的注释（允许旋转 + configChanges）。
 */
class MygoActivity : Activity() {

    private lateinit var view: MygoView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // ActionBar 不要：本 app 自己画顶栏（lab/shell.go）。主题里也写了
        // NoActionBar，这里是同一件事的代码侧 —— 两处都留是因为主题可以被
        // 换掉，而这行不会。
        requestWindowFeature(Window.FEATURE_NO_TITLE)
        // 渲染场景常亮；避免用户触摸时息屏把 surface 带走。
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)

        // 🔴 这里**不**加 FLAG_FULLSCREEN，也不碰 systemUiVisibility：
        // 这是个普通 app，状态栏和手势条都要显示（主题见 AndroidManifest.xml
        // 的 MygoTheme）。要隐藏系统栏的是全屏类 app（播放器、游戏），
        // 那应当由那个 Activity 自己声明，而不是这个宿主默认替所有 app 决定。
        //
        // 🔴 layoutInDisplayCutoutMode 在**代码**里设，不在主题里：
        // 它不是 style 属性，是 WindowManager.LayoutParams 的一个字段 —— 写成
        // `<item name="android:layoutInDisplayCutoutMode">` 会让 aapt 报
        // "style attribute not found"（实测），因为那个名字在 attr 表里只有
        // LayoutParams 用得到。
        //
        // SHORT_EDGES = 允许内容画到短边之外的刘海区域，即"背景铺到屏幕边缘"。
        // 这正是「背景铺满 + 内容躲开」要的那一半：这里允许画过去，躲多少由
        // Go 侧的 SafeArea 决定（见 MygoView.reportSafeArea）。默认的
        // DEFAULT 会在有刘海的设备上把窗口整个裁到刘海以下 —— 背景也铺不过去，
        // 屏幕顶部会出现一条与 app 无关的颜色带。
        //
        // 有刘海才有区别。本机（PKJ110）是挖孔屏：dumpsys 报
        // DisplayCutout insets=Rect(0,160-0,0)，即顶部 160px（40 DIP）的挖孔区，
        // 与状态栏同高，所以两种模式在本机看着一样。
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            window.attributes = window.attributes.apply {
                layoutInDisplayCutoutMode =
                    WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES
            }
        }

        // 🔴 让窗口自己处理 insets。targetSdk 36 强制 edge-to-edge，但如果不
        // 明确说不 fit，平台仍按旧行为把内容挤开 —— 而在这个过程中
        // `WindowInsets.Type.ime()` 不会派发到 View：键盘"已显示"
        // （mInputShown=true、IME 窗口有 surface），insets 里却始终没有它，
        // 于是没有任何东西让位，键盘被压在内容下面看不见。
        //
        // false = 内容画到系统栏与键盘下面，各让多少由 Go 侧的布局按
        // MygoView.reportSafeArea 报上来的值做 —— 与本文件上面
        // layoutInDisplayCutoutMode 的分工一致：这里只允许画过去，躲多少在 Go。
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            window.setDecorFitsSystemWindows(false)
        }

        view = MygoView(this)
        // 单层 View 层级：SurfaceView 直接作为 content view。
        setContentView(view)

        // 让 View 拿到焦点，否则 onKeyDown/onKeyUp 不会派发到它。
        view.grabInputFocus()

        // 🔴 图片服务地址不在这里传了。
        //
        // 以前是 gradle -PmygoLabList → BuildConfig.LAB_LIST → Os.setenv →
        // Go 的 os.Getenv：把编译期常量绕两个运行时弯交给 Go，还带一个
        // 「setenv 必须早于 loadLibrary」的时序雷（Go 在包 init 里读，晚一步
        // 就静默为空）。
        //
        // 现在它由 lab 自己的构建脚本编进 .so：
        //   MYGO_LAB_LIST=http://<主机>:8098 bash build.sh android
        // 见 mygo-wind-router-lab/lab/listbase_android.go。地址随网络变、
        // 不随运行变，而那个 .so 本来就是「为拿 adb 的这台机器」编的。

        // 平台能力（剪贴板/URL/通知/无障碍）的 Java 侧握手；必须在任何平台能力被使用前调用。
        MygoPlatform.install(this, view)
    }

    /**
     * 把系统应用的返回结果转给平台侧：扫码就是从这里回到 Go 的。
     *
     * 用旧 API 而不是 registerForActivityResult：这里只转发，不关心生命周期，
     * 而旧 API 在 `Activity`（非 ComponentActivity）上不需要额外依赖。
     */
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        MygoPlatform.onScanResult(requestCode, resultCode, data)
    }

    override fun onResume() {
        super.onResume()
        // 回到前台后焦点可能被系统收走，重新要一次；这里**不**做任何 surface 生命周期动作。
        view.grabInputFocus()
    }

    // 有意不重写 onDestroy 去释放 Go 侧状态：Go 侧负责保活。
    // 若将来需要「App 真正退出才释放」，应在 Application/进程级做，而不是 Activity 级。
}
