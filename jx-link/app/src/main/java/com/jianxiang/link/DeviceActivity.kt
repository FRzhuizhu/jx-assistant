package com.jianxiang.link

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.graphics.Color
import android.net.Uri
import android.os.Bundle
import android.view.MotionEvent
import android.view.View
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
 * - 应用级「首页」浮条（2026-10-12 改造）：原生 TextView 胶囊浮在 WebView 之上、
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
            }

            override fun onReceivedError(v: WebView, req: WebResourceRequest, err: WebResourceError) {
                super.onReceivedError(v, req, err)
                if (req.isForMainFrame && req.url.toString() == loadUrlNow) {
                    runOnUiThread { showFail() }
                }
            }
        }

        web.webChromeClient = object : WebChromeClient() {
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
        loadUrlNow = dev.entryUrl()
        web.loadUrl(loadUrlNow)
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

    override fun onBackPressed() {
        // 系统返回 = 回首页（应用内的页面返回用它自己的返回按钮/手势）
        super.onBackPressed()
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
    }
}
