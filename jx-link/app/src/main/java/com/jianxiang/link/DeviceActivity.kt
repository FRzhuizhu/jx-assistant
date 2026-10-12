package com.jianxiang.link

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.SystemClock
import android.view.MotionEvent
import android.view.View
import android.webkit.PermissionRequest
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.LinearLayout
import android.widget.Toast
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import androidx.webkit.ProfileStore
import kotlin.math.abs

/**
 * 电脑页：全屏 WebView 加载远程入口（gate.html 自动经加密中继拉起应用外壳）。
 *
 * - 每台电脑一个独立 WebView Profile（WebView 114+），缓存/localStorage 互不串门；
 *   首页「清空缓存/删除」即删这个 Profile
 * - HTTPS 优先（2026-10-12）：入口先按 https 同端口加载，把该网关加进自签白名单
 *   （onReceivedSslError 对已配对 host 自动「继续访问」）——语音输入需要安全上下文；
 *   网关不支持 https（握手失败/连不上）自动回落 http，一切如旧。浏览器扫码照旧走
 *   http，只有壳子享受 https
 * - 麦克风权限链：Manifest RECORD_AUDIO + onPermissionRequest 对已配对 host 的
 *   AUDIO_CAPTURE 先申请运行时权限再 grant（语音听写的采集入口）
 * - 返回键（2026-10-12）：交给网页的返回键守卫——对话页→呼出左栏、左栏→收回
 *   对话页、弹窗/设置页逐层收敛（网页自带 popstate 两级返回）；3 秒内第二次
 *   返回 = 回壳首页（不能拦住用户退不出去）
 * - 应用级「首页」浮条：原生 TextView 胶囊浮在 WebView 之上、
 *   屏幕水平绝对居中，与网页布局完全解耦——此前把按钮注入网页顶栏，编程模式下
 *   顶栏内容变多会把右侧栏按钮挤到第二行；浮条盖在网页上，网页爱怎么排怎么排
 * - 原生上下边缘手势：从屏幕顶缘往下滑 / 底缘往上滑回首页（参数对齐电脑端左右栏
 *   滑出手势：30dp 热区、56dp 触发、横向意图即让位；只认从边缘起手的滑动）
 * - 系统返回键 = 回首页
 */
class DeviceActivity : Activity() {

    private lateinit var dev: Dev
    private lateinit var web: WebView
    private lateinit var failBox: View
    private var loadUrlNow: String = ""

    // HTTPS 优先状态：先试 https 同端口，主 frame 加载失败即回落 http（一次定论）
    private var httpsTried = false
    private var httpsOk = false

    // 麦克风：网页 PermissionRequest 暂存（等运行时权限结果回来再 grant）
    private var pendingAudioReq: PermissionRequest? = null

    // 返回键：3 秒内第二次 = 回首页
    private var lastBackAt = 0L

    // 边缘手势状态：0=无 1=顶缘 2=底缘
    private var edge = 0
    private var x0 = 0f
    private var y0 = 0f
    private var fired = false

    private var filePathCallback: ValueCallback<Array<Uri>>? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val id = intent.getStringExtra(EXTRA_ID)
        dev = DeviceStore.byId(this, id ?: "").let {
            if (it == null) {
                finish()
                return
            }
            it
        }
        // 记忆位置：退出应用时停留在这台电脑里，下次冷启动直达
        DeviceStore.setLastDevice(this, dev.id)

        setContentView(R.layout.activity_device)
        android.util.Log.d("JxLink", "DeviceActivity onCreate dev=${dev.id}")
        web = findViewById(R.id.web)
        failBox = findViewById(R.id.load_fail)

        // 应用级首页浮条：浮在 WebView 之上、水平绝对居中；点击回首页
        findViewById<android.widget.TextView>(R.id.home_chip).apply {
            text = dev.name
            setOnClickListener { finish() }
        }

        // 每台电脑独立缓存域（WebView 114+；旧版本退化为共享默认域+键级清理）
        if (WebViewFeature.isFeatureSupported(WebViewFeature.MULTI_PROFILE)) {
            try {
                ProfileStore.getInstance().getOrCreateProfile(dev.profileName())
                WebViewCompat.setProfile(web, dev.profileName())
            } catch (e: Exception) {
                // 不支持就共享默认 Profile，功能不受影响
            }
        }

        setupWebView()
        findViewById<Button>(R.id.btn_retry).setOnClickListener {
            hideFail()
            loadEntry()
        }
        findViewById<Button>(R.id.btn_back_home).setOnClickListener { finish() }
        loadEntry()
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun setupWebView() {
        web.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            javaScriptCanOpenWindowsAutomatically = true
            allowFileAccess = false
            allowContentAccess = false
            mediaPlaybackRequiresUserGesture = true
            loadWithOverviewMode = true
            useWideViewPort = true
        }
        web.setBackgroundColor(Color.WHITE)

        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(v: WebView, req: WebResourceRequest): Boolean {
                val u = req.url
                val s = u.scheme ?: return false
                if (s == "http" || s == "https") {
                    // 入口同源（网关/局域网主机）内放行；外链交给系统浏览器
                    val same = u.host == dev.host && portOf(u) == dev.port
                    return !same
                }
                return false // blob:/data: 等放行（远程模式资产即 blob）
            }

            override fun onPageFinished(v: WebView, url: String?) {
                super.onPageFinished(v, url)
                if (url != null && url.startsWith("https://")) httpsOk = true
            }

            override fun onReceivedSslError(v: WebView, handler: android.webkit.SslErrorHandler, error: android.net.http.SslError) {
                // 自签白名单：只放行已配对网关（扫码建立过配对关系的 host），
                // 其余一律走默认拒绝——不放行陌生站点
                val host = try { Uri.parse(error.url).host } catch (e: Exception) { null }
                if (host != null && host.equals(dev.host, ignoreCase = true)) {
                    handler.proceed()
                } else {
                    handler.cancel()
                }
            }

            override fun onReceivedError(v: WebView, req: WebResourceRequest, err: WebResourceError) {
                super.onReceivedError(v, req, err)
                if (req.isForMainFrame && req.url.toString() == loadUrlNow) {
                    fallbackOrFail()
                }
            }
        }

        web.webChromeClient = object : WebChromeClient() {
            override fun onPermissionRequest(req: PermissionRequest) {
                // 语音听写的采集入口：只对已配对 host 放行麦克风（AUDIO_CAPTURE）。
                // 应用须先持 RECORD_AUDIO 运行时权限，才能 grant 给网页
                val host = try { req.origin.host } catch (e: Exception) { null }
                val audio = req.resources.contains(PermissionRequest.RESOURCE_AUDIO_CAPTURE)
                if (audio && host != null && host.equals(dev.host, ignoreCase = true)) {
                    if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
                        req.grant(req.resources)
                    } else {
                        pendingAudioReq = req
                        requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_AUDIO)
                    }
                } else {
                    req.deny()
                }
            }

            override fun onShowFileChooser(
                v: WebView, cb: ValueCallback<Array<Uri>>, params: FileChooserParams,
            ): Boolean {
                filePathCallback?.onReceiveValue(null)
                filePathCallback = cb
                try {
                    val i = Intent(Intent.ACTION_GET_CONTENT)
                    i.addCategory(Intent.CATEGORY_OPENABLE)
                    i.type = "*/*"
                    startActivityForResult(Intent.createChooser(i, "选择文件"), REQ_FILE)
                } catch (e: Exception) {
                    filePathCallback = null
                    return false
                }
                return true
            }
        }
    }

    private fun portOf(u: Uri): Int =
        if (u.port > 0) u.port else (if (u.scheme == "https") 443 else 80)

    private fun loadEntry() {
        // HTTPS 优先：语音输入需要安全上下文；网关多为自签证书——onReceivedSslError
        // 对已配对 host 自动放行。不支持 https（握手/连接失败）由 onReceivedError 回落
        httpsTried = false
        httpsOk = false
        val httpUrl = dev.entryUrl()
        loadUrlNow = if (dev.scheme == "http") {
            httpsTried = true
            "https://" + httpUrl.removePrefix("http://")
        } else {
            httpUrl
        }
        web.loadUrl(loadUrlNow)
    }

    /** 主 frame 加载失败：https 尝试失败 → 回落 http；http 也失败 → 失败浮层 */
    private fun fallbackOrFail() {
        if (httpsTried && !httpsOk) {
            httpsTried = false
            loadUrlNow = dev.entryUrl() // 原 http 入口
            web.loadUrl(loadUrlNow)
            return
        }
        runOnUiThread { showFail() }
    }

    private fun showFail() {
        failBox.visibility = View.VISIBLE
    }

    private fun hideFail() {
        failBox.visibility = View.GONE
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_FILE) {
            val cb = filePathCallback
            filePathCallback = null
            cb?.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(resultCode, data))
        }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQ_AUDIO) {
            val req = pendingAudioReq
            pendingAudioReq = null
            if (grantResults.isNotEmpty() && grantResults[0] == PackageManager.PERMISSION_GRANTED) {
                try { req?.grant(req.resources) } catch (e: Exception) {}
            } else {
                try { req?.deny() } catch (e: Exception) {}
                Toast.makeText(this, "未授予麦克风权限，语音输入不可用", Toast.LENGTH_SHORT).show()
            }
        }
    }

    /* ---------- 返回键：交给网页守卫，双击直出 ---------- */

    override fun onBackPressed() {
        val now = SystemClock.elapsedRealtime()
        if (now - lastBackAt <= 3000) {
            // 3 秒内第二次：回壳首页（不能拦住用户退不出去）
            finish()
            return
        }
        lastBackAt = now
        // 第一次：交给网页的返回键守卫（对话页→呼出左栏 / 左栏→收回对话页 /
        // 弹窗→关弹窗）；设置页走它自己的两级返回（goBack 触发 popstate）
        web.evaluateJavascript(BACK_JS) { res ->
            val r = (res ?: "").trim('"')
            if (r == "settings" && web.canGoBack()) {
                web.goBack()
            } else if (r == "chat") {
                runOnUiThread {
                    Toast.makeText(this, "再按一次返回首页", Toast.LENGTH_SHORT).show()
                }
            }
            // open/close/popover：网页已消费本次返回
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        try {
            (web.parent as? android.view.ViewGroup)?.removeView(web)
            web.destroy()
        } catch (e: Exception) {
        }
    }

    /* ---------- 上下边缘滑动手势（回首页） ---------- */

    private fun dp(v: Float): Float = v * resources.displayMetrics.density

    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        handleEdgeGesture(ev)
        return super.dispatchTouchEvent(ev)
    }

    private fun handleEdgeGesture(ev: MotionEvent) {
        when (ev.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                edge = 0
                fired = false
                if (ev.pointerCount == 1) {
                    // 热区锚定 WebView 实际边界（窗口可能全屏布局、内容带 insets 偏移）
                    val loc = IntArray(2)
                    web.getLocationInWindow(loc)
                    val top = loc[1]
                    val bottom = top + web.height
                    val zone = dp(30f)
                    if (ev.y <= top + zone) edge = 1
                    else if (ev.y >= bottom - zone) edge = 2
                    android.util.Log.d(
                        "JxLink",
                        "down y=${ev.y} top=$top bottom=$bottom zone=$zone edge=$edge",
                    )
                    if (edge != 0) {
                        x0 = ev.x
                        y0 = ev.y
                    }
                }
            }
            MotionEvent.ACTION_POINTER_DOWN -> edge = 0 // 多指：取消
            MotionEvent.ACTION_MOVE -> {
                if (edge != 0 && !fired && ev.pointerCount == 1) {
                    val dx = ev.x - x0
                    val dy = ev.y - y0
                    // 横向意图明显 → 让位（页面里的左右栏滑出、内容横滑）
                    if (abs(dx) > abs(dy) * 1.2f + dp(12f)) {
                        android.util.Log.d("JxLink", "cancel by horizontal dx=$dx dy=$dy")
                        edge = 0
                    } else {
                        val trig = dp(56f)
                        android.util.Log.d("JxLink", "move edge=$edge dx=$dx dy=$dy trig=$trig")
                        if ((edge == 1 && dy >= trig) || (edge == 2 && dy <= -trig)) {
                            fired = true
                            android.util.Log.d("JxLink", "FIRE goHome")
                            finish() // 回首页
                        }
                    }
                }
            }
            MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> edge = 0
        }
    }

    companion object {
        const val EXTRA_ID = "id"
        private const val REQ_FILE = 3
        private const val REQ_AUDIO = 4

        /**
         * 返回键分派脚本（每次即时注入，不驻留——换页/路由重建都不怕）。
         * 优先级复刻网页返回键守卫（app-left.js backGuardPop）：设置页（它自己有
         * 两级 popstate 返回，壳子只负责 goBack 触发）> 弹出菜单（直接关）>
         * 左栏开关（对话页→呼出 / 左栏开→收回，正是窄屏 toggleSidebar 的行为）。
         * 返回值：settings | popover | open | close | chat
         */
        private const val BACK_JS = """
            (function(){
              try {
                var set = document.getElementById('settings');
                if (set && !set.classList.contains('hidden')) return 'settings';
                if (document.querySelector('.popover:not(.hidden)')) {
                  if (typeof closePopovers === 'function') closePopovers();
                  return 'popover';
                }
                if (typeof isNarrow === 'function' && isNarrow() &&
                    typeof toggleSidebar === 'function') {
                  toggleSidebar();
                  return document.body.classList.contains('sb-show') ? 'open' : 'close';
                }
                return 'chat';
              } catch (e) { return 'chat'; }
            })()
        """
    }
}
