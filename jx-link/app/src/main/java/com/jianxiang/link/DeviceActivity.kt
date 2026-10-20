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
import android.os.Handler
import android.os.Looper
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
import android.webkit.WebChromeClient.CustomViewCallback
import android.webkit.URLUtil
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

    // 全屏视频状态（onShowCustomView/onHideCustomView 用）
    private var fullscreenView: View? = null
    private var fullscreenCb: CustomViewCallback? = null

    // 边缘手势状态：0=无 1=顶缘 2=底缘
    private var edge = 0
    private var x0 = 0f
    private var y0 = 0f
    private var fired = false

    private var filePathCallback: ValueCallback<Array<Uri>>? = null

    // ---------- 系统分享送入（来自 ShareActivity） ----------
    // 分享的文件已复制到私有缓存目录；页面（远程外壳）就绪后分块注入：
    // base64 → atob → File → JX.receiveFiles/stageFiles（与 SAF 选择同通路），
    // 文件落进输入框附件区。READY 探测轮询：gate.html 是先加载再异步拉起外壳，
    // onPageFinished ≠ 页面就绪，必须等到 stageFiles 存在为止。
    private var shareDir: java.io.File? = null
    private var shareText: String? = null
    private var shareNames: String = ""   // 仅日志/提示用
    private var shareBusy = false         // 一个轮询循环在跑
    private val shareH = Handler(Looper.getMainLooper())
    private var shareDeadline = 0L

    private fun takeShareExtras(i: Intent?) {
        val dirPath = i?.getStringExtra(EXTRA_SHARE_DIR)
        val txt = i?.getStringExtra(EXTRA_SHARE_TEXT)
        if (dirPath != null) {
            val d = java.io.File(dirPath)
            if (d.isDirectory && d.listFiles()?.isNotEmpty() == true) shareDir = d
        }
        if (!txt.isNullOrBlank()) shareText = txt
        shareNames = i?.getStringExtra("share_names") ?: shareNames
        if (shareDir != null || shareText != null) armShareDelivery()
    }

    /** 页面就绪轮询（每次页面加载完成都续一轮窗口） */
    private fun armShareDelivery() {
        if (shareDir == null && shareText == null) return
        shareDeadline = SystemClock.elapsedRealtime() + SHARE_WAIT_MS
        if (shareBusy) return
        shareBusy = true
        stepShare()
    }

    private fun stepShare() {
        if (shareDir == null && shareText == null) { shareBusy = false; return }
        if (SystemClock.elapsedRealtime() > shareDeadline) {
            shareBusy = false
            Toast.makeText(this, "电脑页迟迟未就绪，本次分享没有送入（页面恢复后请重新分享）", Toast.LENGTH_LONG).show()
            return
        }
        web.evaluateJavascript(JS_SHARE_READY) { res ->
            if (isFinishing || isDestroyed) return@evaluateJavascript
            if ("ready" == (res ?: "").trim('"')) deliverShare()
            else shareH.postDelayed({ if (shareBusy) stepShare() }, 700)
        }
    }

    /** 注入：文件分块喂 base64，全部入队后一次性 commit（evaluateJavascript 按
     *  调用顺序串行执行，无需逐块等回调） */
    private fun deliverShare() {
        val dir = shareDir
        val txt = shareText
        shareDir = null
        shareText = null
        Thread {
            val files = dir?.listFiles()?.sortedBy { it.name } ?: emptyList()
            var readable = 0
            val chunks = ArrayList<String>(files.size * 2 + 4)
            chunks.add("window.__jxsh={q:[],cur:null};")
            for (f in files) {
                val bytes = try { f.readBytes() } catch (e: Exception) { null }
                if (bytes == null || bytes.isEmpty()) continue
                readable++
                val ext = f.name.substringAfterLast('.', "").lowercase()
                val mime = android.webkit.MimeTypeMap.getSingleton()
                    .getMimeTypeFromExtension(ext) ?: "application/octet-stream"
                val quotedName = org.json.JSONObject.quote(f.name)
                val quotedMime = org.json.JSONObject.quote(mime)
                var off = 0
                var first = true
                while (off < bytes.size) {
                    val len = minOf(SHARE_CHUNK_BYTES, bytes.size - off)
                    val b64 = android.util.Base64.encodeToString(bytes, off, len, android.util.Base64.NO_WRAP)
                    chunks.add(
                        if (first) "window.__jxsh.cur={name:$quotedName,mime:$quotedMime,size:${bytes.size},b64:\"$b64\"};"
                        else "if(window.__jxsh&&window.__jxsh.cur)window.__jxsh.cur.b64+=\"$b64\";",
                    )
                    first = false
                    off += len
                }
                chunks.add(JS_SHARE_PUSH_FILE)
            }
            val commit = if (txt != null && txt.isNotBlank()) {
                JS_SHARE_COMMIT.replace("__JX_TXT__", org.json.JSONObject.quote(txt))
            } else JS_SHARE_COMMIT.replace("__JX_TXT__", "null")
            chunks.add(commit)
            for (c in chunks) {
                val js = c
                shareH.post { if (!isFinishing && !isDestroyed) web.evaluateJavascript(js, null) }
            }
            // commit 的回执在最后一块上拿：单独 post 一段带回调的执行
            shareH.post {
                if (isFinishing || isDestroyed) return@post
                web.evaluateJavascript(JS_SHARE_COUNT) { res ->
                    val n = (res ?: "").trim().trim('"').toIntOrNull() ?: -1
                    android.util.Log.d("JxLink", "share delivered: files=$readable ret=$n")
                    runOnUiThread {
                        if (isFinishing || isDestroyed) return@runOnUiThread
                        when {
                            n >= 0 && (readable > 0 || !txt.isNullOrBlank()) -> {
                                val parts = ArrayList<String>()
                                if (readable > 0) parts.add("${if (n > 0) n else readable} 个文件已放进输入框")
                                if (!txt.isNullOrBlank()) parts.add("分享的文字已放进输入框")
                                Toast.makeText(this, parts.joinToString("，"), Toast.LENGTH_LONG).show()
                            }
                            else -> Toast.makeText(this, "分享送入失败（电脑端页面异常）", Toast.LENGTH_LONG).show()
                        }
                        // 清掉缓存目录；若期间又来了新分享，接着送
                        Thread { dir?.deleteRecursively() }.start()
                        if (shareDir != null || shareText != null) { shareBusy = false; armShareDelivery() }
                        else shareBusy = false
                    }
                }
            }
        }.start()
    }

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

        // 预申请麦克风（2026-10-12 修）：首次进设备页就把系统权限框弹掉，
        // 消除「网页请求麦克风 → 系统框弹出 → 用户切去设置 → 网页侧超时被拒」
        // 的竞态；已授予时此调用无感。之后网页 getUserMedia 到来即 grant
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_AUDIO_PRE)
        }
        // Android 13+：下载完成通知权限（拒绝只是少个通知，下载本身不受阻）
        if (Build.VERSION.SDK_INT >= 33 &&
            checkSelfPermission("android.permission.POST_NOTIFICATIONS") != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf("android.permission.POST_NOTIFICATIONS"), REQ_AUDIO_PRE)
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
        takeShareExtras(intent) // 系统分享送入（冷启动路径）
        loadEntry()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        takeShareExtras(intent) // 已在设备页时分享直达（不重载网页）
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
                // gate.html 还要经中继异步拉起应用外壳：onPageFinished 只是窗口续期，
                // 真正就绪由 READY 探测（stageFiles 出现）判定
                armShareDelivery()
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
                if (req.isForMainFrame && sameEntry(req.url, loadUrlNow)) {
                    fallbackOrFail()
                }
            }
        }

        web.webChromeClient = object : WebChromeClient() {
            override fun onConsoleMessage(msg: android.webkit.ConsoleMessage): Boolean {
                // 网页 console 转发到 logcat（tag JxLinkJS）：远程页面静默失败时定位用
                android.util.Log.d(
                    "JxLinkJS",
                    "${msg.message()} @ ${msg.sourceId()}:${msg.lineNumber()}",
                )
                return true
            }

            override fun onPermissionRequest(req: PermissionRequest) {
                // 语音听写的采集入口：只对已配对 host 放行麦克风（AUDIO_CAPTURE）。
                // 应用须先持 RECORD_AUDIO 运行时权限，才能 grant 给网页。
                // origin 可能是 blob:https://host:port/uuid（P2P 直连模式的远程外壳
                // 页面挂在 blob origin 上）——Uri.parse 对 blob: 取 host 是 null，
                // 必须剥掉 blob: 前缀再解析，否则会被误判成陌生站点而 deny
                val u = req.origin
                val host = try {
                    if (u.scheme == "blob") Uri.parse(u.toString().removePrefix("blob:")).host
                    else u.host
                } catch (e: Exception) { null }
                val audio = req.resources.contains(PermissionRequest.RESOURCE_AUDIO_CAPTURE)
                if (audio && host != null && host.equals(dev.host, ignoreCase = true)) {
                    if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
                        req.grant(req.resources)
                    } else {
                        pendingAudioReq = req
                        requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_AUDIO)
                    }
                } else {
                    android.util.Log.d("JxLink", "deny permission origin=$u host=$host devHost=${dev.host}")
                    req.deny()
                }
            }

            override fun onShowFileChooser(
                v: WebView, cb: ValueCallback<Array<Uri>>, params: FileChooserParams,
            ): Boolean {
                // 用 FileChooserParams.createIntent（带网页声明的 accept 类型），
                // 兜底 ACTION_GET_CONTENT 全类型。系统 SAF 选择器自带全部权限，
                // 应用自身无需任何存储权限
                filePathCallback?.onReceiveValue(null)
                filePathCallback = cb
                try {
                    val i = try { params.createIntent() } catch (e: Exception) {
                        Intent(Intent.ACTION_GET_CONTENT).apply {
                            addCategory(Intent.CATEGORY_OPENABLE)
                            type = "*/*"
                        }
                    }
                    startActivityForResult(Intent.createChooser(i, "选择文件"), REQ_FILE)
                } catch (e: Exception) {
                    filePathCallback = null
                    return false
                }
                return true
            }

            // 全屏视频（网页 requestFullscreen）：WebView 默认无响应，须实现
            // onShowCustomView。最简版：盖满 Activity 根布局，退出还原
            override fun onShowCustomView(view: View, callback: CustomViewCallback) {
                if (fullscreenView != null) {
                    callback.onCustomViewHidden()
                    return
                }
                fullscreenView = view
                fullscreenCb = callback
                (window.decorView as? android.widget.FrameLayout)?.addView(
                    view,
                    android.widget.FrameLayout.LayoutParams(
                        android.view.ViewGroup.LayoutParams.MATCH_PARENT,
                        android.view.ViewGroup.LayoutParams.MATCH_PARENT,
                    ),
                )
                web.visibility = View.GONE
                findViewById<android.widget.TextView>(R.id.home_chip).visibility = View.GONE
                window.addFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
            }

            override fun onHideCustomView() {
                val v = fullscreenView ?: return
                fullscreenView = null
                (window.decorView as? android.widget.FrameLayout)?.removeView(v)
                web.visibility = View.VISIBLE
                findViewById<android.widget.TextView>(R.id.home_chip).visibility = View.VISIBLE
                window.clearFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
                fullscreenCb?.onCustomViewHidden()
                fullscreenCb = null
            }
        }

        // 下载：WebView 默认点下载链接无响应，必须挂 DownloadListener →
        // 系统 DownloadManager 落到公共 Downloads（下载不需要应用存储权限）
        web.setDownloadListener { url, _, contentDisposition, mimeType, _ ->
            try {
                val req = android.app.DownloadManager.Request(Uri.parse(url))
                    .setMimeType(mimeType)
                    .setNotificationVisibility(android.app.DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
                val name = URLUtil.guessFileName(url, contentDisposition, mimeType)
                req.setDestinationInExternalPublicDir(
                    android.os.Environment.DIRECTORY_DOWNLOADS, name,
                )
                val cookies = android.webkit.CookieManager.getInstance().getCookie(url)
                if (cookies != null) req.addRequestHeader("cookie", cookies)
                val dm = getSystemService(DOWNLOAD_SERVICE) as android.app.DownloadManager
                dm.enqueue(req)
                android.widget.Toast.makeText(this, "开始下载：$name", android.widget.Toast.LENGTH_SHORT).show()
            } catch (e: Exception) {
                android.util.Log.d("JxLink", "download fail: ${e.message}")
                android.widget.Toast.makeText(this, "下载失败：${e.message}", android.widget.Toast.LENGTH_SHORT).show()
            }
        }
    }

    private fun portOf(u: Uri): Int =
        if (u.port > 0) u.port else (if (u.scheme == "https") 443 else 80)

    /** 主 frame 失败判定：Chromium 会把无路径 URL 规范化成带尾斜杠、剥掉
     *  fragment——字符串全等会漏判（LAN 直连入口无路径，WebView 124 实测
     *  ERR_SSL_PROTOCOL_ERROR 后回落不触发），按 host+port+路径判 */
    private fun sameEntry(u: Uri, entry: String): Boolean {
        val e = try { Uri.parse(entry) } catch (ex: Exception) { return false }
        if ((u.host ?: "") != (e.host ?: "") || portOf(u) != portOf(e)) return false
        val up = u.path ?: "/"
        val ep = (e.path ?: "/").ifEmpty { "/" }
        return up.trimEnd('/') == ep.trimEnd('/')
    }

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
        if (requestCode == REQ_AUDIO || requestCode == REQ_AUDIO_PRE) {
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
        // 全屏视频先退全屏（不退出页面）
        if (fullscreenView != null) {
            web.webChromeClient?.onHideCustomView()
            return
        }
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

    /**
     * 冷路径（进程被杀、系统按 saved state 重建本页）故意不保存任何实例状态：
     * WebView 默认会把自己的整页状态塞进 Activity 的 saved state，重建时「原页直出」
     * ——等于跳过 gate 中转页裸进网页（远程外壳的最终 URL 挂在 blob origin 上，
     * 直出的页面没经过中继握手，待办/语音等组件会随机初始化不全，2026-10-20 用户实测）。
     * 这里连 super 都不调：重建时 onCreate 照常 loadEntry() 走中转页完整初始化，
     * 那是已知的好路径——宁走中转页，不裸进网页。
     */
    override fun onSaveInstanceState(outState: Bundle) {
        // 有意留空（详见上方注释）
    }

    override fun onDestroy() {
        super.onDestroy()
        shareH.removeCallbacksAndMessages(null)
        // 全屏视频残留兜底（正在全屏时退出页面）
        fullscreenView?.let {
            (window.decorView as? android.widget.FrameLayout)?.removeView(it)
            fullscreenView = null
        }
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
        const val EXTRA_SHARE_DIR = "share_dir"   // ShareActivity → 待送入的缓存目录
        const val EXTRA_SHARE_TEXT = "share_text" // ShareActivity → 待送入的分享文本
        private const val REQ_FILE = 3
        private const val REQ_AUDIO = 4
        private const val REQ_AUDIO_PRE = 5 // 进设备页预申请（结果与网页请求共用一段处理）
        private const val SHARE_WAIT_MS = 120_000L // 页面就绪等待窗口（中继拉起外壳可能要十几秒）
        private const val SHARE_CHUNK_BYTES = 384 * 1024 // base64 分块：每块注入 ~512KB 字符串

        /** 就绪探测：远程外壳把它俩之一暴露出来才算页面可用（gate 先载、外壳后到） */
        private const val JS_SHARE_READY = """
            (function(){try{
              if(window.JX&&typeof JX.receiveFiles==='function')return 'ready';
              if(typeof stageFiles==='function')return 'ready';
              return 'wait';
            }catch(e){return 'wait'}})()
        """

        /** 单文件收尾：cur 的 base64 解码成 File 入队 */
        private const val JS_SHARE_PUSH_FILE = """
            (function(){var s=window.__jxsh;if(!s||!s.cur)return -1;var c=s.cur;s.cur=null;
            try{var bin=atob(c.b64);c.b64=null;var u=new Uint8Array(bin.length);
            for(var i=0;i<bin.length;i++)u[i]=bin.charCodeAt(i);
            s.q.push(new File([u],c.name,{type:c.mime||'application/octet-stream'}));
            return u.length}catch(e){return -2}})()
        """

        /** 提交：文件走 JX.receiveFiles（老网页回退全局 stageFiles），文本追加进主输入框 */
        private const val JS_SHARE_COMMIT = """
            (function(){var s=window.__jxsh;window.__jxsh=null;window.__jxshDone={n:-9};if(!s)return -1;
            var q=s.q||[];var n=0;
            if(q.length){
              var f=(window.JX&&typeof JX.receiveFiles==='function')?JX.receiveFiles:
                    (typeof stageFiles==='function'?stageFiles:null);
              if(!f)return -2;
              var r=f(q);n=r?r.length:0;
            }
            var t=__JX_TXT__;
            if(t){
              var g=(window.JX&&typeof JX.receiveText==='function')?JX.receiveText:null;
              if(g){g(t)}
              else{var ta=document.getElementById('input');
                if(ta){ta.value=(ta.value?ta.value+'\n':'')+t;
                  ta.dispatchEvent(new Event('input',{bubbles:true}));}}
            }
            window.__jxshDone={n:n};
            return n})()
        """

        /** 入队计数（commit 后查询回执用） */
        private const val JS_SHARE_COUNT = """
            (function(){return (window.__jxshDone&&window.__jxshDone.n)||0})()
        """

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
