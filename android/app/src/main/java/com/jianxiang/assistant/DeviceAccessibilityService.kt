package com.jianxiang.assistant

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.graphics.Bitmap
import android.graphics.Path
import android.graphics.Rect
import android.os.Build
import android.os.Bundle
import android.util.Base64
import android.view.accessibility.AccessibilityNodeInfo
import java.io.ByteArrayOutputStream

/**
 * 设备无障碍服务：手机操控三件套里「截屏 + 界面操作」的执行体。
 * 引擎侧 device_* 工具经设备桥（DeviceBridge 轮询引擎 /api/device/poll）
 * 下发命令，这里逐条执行。刻意不订阅无障碍事件（onAccessibilityEvent 空
 * 实现）——界面树按需从 rootInActiveWindow 现取，不做常驻缓存。
 *
 * 用户开启路径：设置 → 无障碍 → 已下载的应用 → 数字助理。
 */
class DeviceAccessibilityService : AccessibilityService() {

    companion object {
        @Volatile
        internal var svcInst: DeviceAccessibilityService? = null

        /** 服务是否已连接（设备桥轮询时上报给引擎，环境快照据此公告）。 */
        val connected: Boolean get() = svcInst != null
    }

    override fun onServiceConnected() {
        super.onServiceConnected()
        svcInst = this
    }

    override fun onDestroy() {
        svcInst = null
        super.onDestroy()
    }

    override fun onAccessibilityEvent(event: android.view.accessibility.AccessibilityEvent?) {
        // 不订阅：能力全部按需拉取（rootInActiveWindow / dispatchGesture / takeScreenshot）
    }

    override fun onInterrupt() {}

    // ---------- 截屏（API 30+；无障碍通道免 MediaProjection 授权弹窗） ----------

    fun screenshot(done: (ok: Boolean, b64: String?, w: Int, h: Int, err: String?) -> Unit) {
        if (Build.VERSION.SDK_INT < 30) {
            done(false, null, 0, 0, "无障碍截屏需要 Android 11+")
            return
        }
        takeScreenshot(android.view.Display.DEFAULT_DISPLAY, mainExecutor,
            object : TakeScreenshotCallback {
                override fun onSuccess(screenshot: ScreenshotResult) {
                    try {
                        val hb = screenshot.hardwareBuffer
                            ?: return done(false, null, 0, 0, "hardwareBuffer 为空")
                        val hw = Bitmap.wrapHardwareBuffer(hb, screenshot.colorSpace)
                            ?: return done(false, null, 0, 0, "Bitmap 包装失败").also { hb.close() }
                        val soft = hw.copy(Bitmap.Config.ARGB_8888, false)
                        hw.recycle()
                        hb.close()
                        val baos = ByteArrayOutputStream()
                        soft.compress(Bitmap.CompressFormat.PNG, 90, baos)
                        val b64 = Base64.encodeToString(baos.toByteArray(), Base64.NO_WRAP)
                        val w = soft.width
                        val h = soft.height
                        soft.recycle()
                        done(true, b64, w, h, null)
                    } catch (e: Exception) {
                        done(false, null, 0, 0, "" + e.message)
                    }
                }

                override fun onFailure(errorCode: Int) {
                    done(false, null, 0, 0, "截屏错误 code=$errorCode")
                }
            })
    }

    // ---------- 手势（点按 / 滑动；坐标为屏幕像素，与截屏 1:1） ----------

    fun tap(x: Float, y: Float, done: (Boolean, String?) -> Unit) {
        val path = Path().apply { moveTo(x, y) }
        dispatch(path, 60, done)
    }

    fun swipe(x1: Float, y1: Float, x2: Float, y2: Float, duration: Long, done: (Boolean, String?) -> Unit) {
        val path = Path().apply { moveTo(x1, y1); lineTo(x2, y2) }
        dispatch(path, duration, done)
    }

    private fun dispatch(path: Path, duration: Long, done: (Boolean, String?) -> Unit) {
        val g = GestureDescription.Builder()
            .addStroke(GestureDescription.StrokeDescription(path, 0, duration))
            .build()
        val ok = dispatchGesture(g, object : GestureResultCallback() {
            override fun onCompleted(gestureDescription: GestureDescription?) { done(true, null) }
            override fun onCancelled(gestureDescription: GestureDescription?) { done(false, "手势被系统取消") }
        }, null)
        if (!ok) done(false, "手势分发失败")
    }

    // ---------- 系统键（全局动作） ----------

    fun key(k: String): String? {
        val a = when (k) {
            "back" -> GLOBAL_ACTION_BACK
            "home" -> GLOBAL_ACTION_HOME
            "recents" -> GLOBAL_ACTION_RECENTS
            "notifications" -> GLOBAL_ACTION_NOTIFICATIONS
            else -> return "未知系统键：$k（支持 back/home/recents/notifications）"
        }
        return if (performGlobalAction(a)) null else "执行失败"
    }

    // ---------- 文本输入（焦点输入框整框替换） ----------

    fun type(text: String): String? {
        val root = rootInActiveWindow ?: return "无活动窗口（先点按目标输入框获得焦点）"
        val node = root.findFocus(AccessibilityNodeInfo.FOCUS_INPUT)
            ?: return "没有持有焦点的输入框（先 device_tap 点一下目标输入框）"
        val args = Bundle()
        args.putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, text)
        return if (node.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, args)) null
        else "输入框不接受整框替换（ACTION_SET_TEXT 被拒绝）"
    }

    // ---------- 界面树（可交互元素 + 像素 bounds） ----------

    fun uiDump(): String {
        val root = rootInActiveWindow ?: return "（无活动窗口）"
        val sb = StringBuilder()
        fun walk(n: AccessibilityNodeInfo, depth: Int) {
            if (depth > 25 || sb.length > 20000) return
            val r = Rect()
            n.getBoundsInScreen(r)
            val clickable = if (n.isClickable) "[可点]" else ""
            val txt = n.text?.toString()?.replace("\n", " ")?.take(60)
            val desc = n.contentDescription?.toString()?.replace("\n", " ")?.take(60)
            if (clickable.isNotEmpty() || !txt.isNullOrEmpty() || !desc.isNullOrEmpty()) {
                sb.append("  ".repeat(depth))
                sb.append(clickable)
                sb.append("[${r.left},${r.top},${r.right},${r.bottom}] ")
                sb.append(listOfNotNull(txt?.ifEmpty { null }, desc?.ifEmpty { null }).joinToString(" / "))
                sb.append(" <${n.className}>\n")
            }
            for (i in 0 until n.childCount) {
                n.getChild(i)?.let { walk(it, depth + 1) }
            }
        }
        walk(root, 0)
        return sb.toString().ifEmpty { "（当前界面没有可交互的文本元素，可先截屏看全貌）" }
    }
}
