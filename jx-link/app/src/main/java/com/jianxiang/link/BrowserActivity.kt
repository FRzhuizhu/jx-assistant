package com.jianxiang.link

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.graphics.Color
import android.net.Uri
import android.os.Bundle
import android.view.MotionEvent
import android.view.View
import android.view.ViewGroup
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.TextView
import java.io.File
import kotlin.math.abs

/**
 * 壳内浏览器页：手机端「打开一个页面」的落点（右栏浏览器的网址与文件预览、
 * 对话正文里的链接都开在这里）。
 *
 * 为什么要有这一层：手机壳的 WebView 没有接管新窗口（没实现
 * WebChromeClient.onCreateWindow），网页侧能做的只有 window.open(url,'_blank')
 * ——这颗请求要么被 WebView 悄悄丢掉（点了没反应），要么在当前 WebView 里把
 * 应用 UI 顶掉：那页上没有 jxNavBack，返回键什么都不做，用户只能杀掉整个 App。
 * 所以手机端一律在壳里另开这一层：返回键与右缘左滑由它自己收掉（先退本页
 * 历史，退不动就 finish 回对话页），**不碰**电脑页那套 jxNavBack 的层级。
 */
class BrowserActivity : Activity() {

    private lateinit var web: WebView
    private lateinit var titleView: TextView
    private lateinit var btnPrev: TextView
    private lateinit var btnNext: TextView
    private var contentFile: File? = null

    // 右缘左滑关闭的手势状态
    private var edgeHit = false
    private var edgeX0 = 0f
    private var edgeY0 = 0f

    // 本页里点进去的链接数：决定返回是「退这一步」还是「关掉这一层」
    // （服务器重定向/页面自身跳转不计数，否则按返回只退了一次跳转，像没反应）
    private var forwardLinks = 0

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_browser)
        web = findViewById(R.id.web)
        titleView = findViewById(R.id.page_title)
        btnPrev = findViewById(R.id.btn_prev)
        btnNext = findViewById(R.id.btn_next)

        findViewById<View>(R.id.btn_close).setOnClickListener { finish() }
        findViewById<View>(R.id.btn_reload).setOnClickListener { web.reload() }
        // 工具栏只是本页历史的导航（没上一级时按钮是灰的）；关掉这一层归 ✕ / 返回键
        btnPrev.setOnClickListener {
            if (canStepBack()) {
                forwardLinks--
                web.goBack()
            }
        }
        btnNext.setOnClickListener {
            if (web.canGoForward()) {
                forwardLinks++
                web.goForward()
            }
        }

        setupWebView()
        syncBar() // 进来先灰掉后退/前进（此时无历史）

        val url = intent.getStringExtra(EXTRA_URL)
        val path = intent.getStringExtra(EXTRA_FILE)
        when {
            !path.isNullOrEmpty() -> {
                // 内容预览：隧道取来的字节已落成私有缓存文件，file:// 直接打开
                contentFile = File(path)
                titleView.text = intent.getStringExtra(EXTRA_NAME)?.ifEmpty { null } ?: "预览"
                web.loadUrl("file://$path")
            }
            !url.isNullOrEmpty() -> {
                titleView.text = url
                web.loadUrl(url)
            }
            else -> finish()
        }
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun setupWebView() {
        web.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            loadWithOverviewMode = true
            useWideViewPort = true
            mediaPlaybackRequiresUserGesture = true
            allowFileAccess = true // 内容预览走缓存文件 file://（API 30+ 默认关）
            // 本页里再 window.open 一律在当前页导航：壳只开这一层，返回键总能
            // goBack 回来，不会再出现「又开一层、谁也关不掉」
            setSupportMultipleWindows(false)
            javaScriptCanOpenWindowsAutomatically = false
        }
        web.setBackgroundColor(Color.WHITE)

        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(v: WebView, req: WebResourceRequest): Boolean {
                val s = req.url.scheme ?: return false
                if (s == "http" || s == "https" || s == "file") {
                    // 用户在这页里点进去的链接才算「有上一级」：服务器重定向、页面
                    // 自己跳转不计数，免得按返回只退了一次跳转、看着像没反应
                    if (s != "file") forwardLinks++
                    return false
                }
                // tel:/mailto:/微信 一类交给系统；没有应用能接就安静地留在这一页
                try {
                    startActivity(Intent(Intent.ACTION_VIEW, req.url))
                } catch (e: Exception) {
                    android.util.Log.d("JxLink", "browser page: no handler for $s")
                }
                return true
            }

            override fun onPageFinished(v: WebView, url: String?) {
                super.onPageFinished(v, url)
                syncBar()
            }
        }

        web.webChromeClient = object : WebChromeClient() {
            override fun onReceivedTitle(v: WebView, t: String?) {
                if (!t.isNullOrBlank()) titleView.text = t
            }
        }
    }

    private fun syncBar() {
        val back = canStepBack()
        val fwd = web.canGoForward()
        btnPrev.isEnabled = back
        btnPrev.alpha = if (back) 1f else 0.3f
        btnNext.isEnabled = fwd
        btnNext.alpha = if (fwd) 1f else 0.3f
    }

    private fun canStepBack(): Boolean = forwardLinks > 0 && web.canGoBack()

    /* ---------- 返回与手势：这一层的关闭只在这里决定 ---------- */

    override fun onBackPressed() {
        // 先在「本页里点进去的链接」上退，退不动就关掉这一层回对话页。壳层的事壳里
        // 收尾——不 evaluateJavascript 调 jxNavBack（那是电脑页的层级，在这一层上
        // 调用只会把用户的返回吃掉）
        if (canStepBack()) {
            forwardLinks--
            web.goBack()
            return
        }
        finish()
    }

    private fun dp(v: Float): Float = v * resources.displayMetrics.density

    override fun dispatchTouchEvent(ev: MotionEvent): Boolean {
        // 右缘起手向左滑 = 关掉本层（与网页右栏手势同语义：从边缘把当前层滑走）
        val w = resources.displayMetrics.widthPixels
        when (ev.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                edgeHit = ev.pointerCount == 1 && ev.x >= w - dp(24f)
                edgeX0 = ev.x
                edgeY0 = ev.y
            }
            MotionEvent.ACTION_POINTER_DOWN -> edgeHit = false
            MotionEvent.ACTION_MOVE -> {
                if (edgeHit) {
                    val dx = ev.x - edgeX0 // 负 = 向左
                    val dy = ev.y - edgeY0
                    if (dx < -dp(80f) && abs(dx) > abs(dy) * 2) {
                        edgeHit = false
                        finish()
                        return true
                    }
                }
            }
            MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> edgeHit = false
        }
        return super.dispatchTouchEvent(ev)
    }

    override fun onDestroy() {
        super.onDestroy()
        try {
            (web.parent as? ViewGroup)?.removeView(web)
            web.destroy()
        } catch (e: Exception) {
        }
        // 内容预览的缓存文件只服务这一页：用户真关掉才删（系统回收重建时文件还得在）
        if (isFinishing) contentFile?.delete()
    }

    companion object {
        private const val EXTRA_URL = "url"
        private const val EXTRA_FILE = "file"
        private const val EXTRA_NAME = "name"
        private const val VIEW_DIR = "browser-view"
        private const val VIEW_KEEP_MS = 3600_000L // 一小时后清残留（进程被杀没走到删除）

        fun forUrl(from: Activity, url: String): Intent =
            Intent(from, BrowserActivity::class.java).putExtra(EXTRA_URL, url)

        fun forContent(from: Activity, file: File, name: String?): Intent =
            Intent(from, BrowserActivity::class.java)
                .putExtra(EXTRA_FILE, file.absolutePath)
                .putExtra(EXTRA_NAME, name ?: "")

        /**
         * 网页交来的内容（隧道/blob 里拿到的字节，base64）落成私有缓存文件：
         * 内容预览用 file:// 打开，Intent 只背一个路径——几 MB 的 base64 塞进
         * Intent extra 会撞 Binder 的体积上限（TransactionTooLarge）。
         */
        fun writeContent(ctx: Context, name: String?, mime: String?, base64: String): File {
            val dir = File(ctx.cacheDir, VIEW_DIR).apply { mkdirs() }
            val now = System.currentTimeMillis()
            dir.listFiles()?.forEach { if (now - it.lastModified() > VIEW_KEEP_MS) it.delete() }
            val ext = extOf(name) ?: extOfMime(mime)
            val f = File(dir, "view-$now" + if (ext.isEmpty()) "" else ".$ext")
            f.writeBytes(android.util.Base64.decode(base64, android.util.Base64.DEFAULT))
            return f
        }

        private fun extOf(name: String?): String? {
            val n = name ?: return null
            val i = n.lastIndexOf('.')
            if (i <= 0 || i == n.length - 1) return null
            val e = n.substring(i + 1).lowercase()
            return if (e.length in 1..5 && e.all { it.isLetterOrDigit() }) e else null
        }

        // 文件名没扩展名时（导出接口的 basename 就是 export 一类）按 Content-Type 补：
        // file:// 打开完全靠扩展名判类型，缺了会把网页当纯文本显示
        private fun extOfMime(mime: String?): String {
            val m = (mime ?: "").substringBefore(';').trim().lowercase()
            return when (m) {
                "text/html", "application/xhtml+xml" -> "html"
                "application/json" -> "json"
                "text/plain" -> "txt"
                "application/xml", "text/xml" -> "xml"
                "application/pdf" -> "pdf"
                "image/png" -> "png"
                "image/jpeg" -> "jpg"
                "image/gif" -> "gif"
                "image/webp" -> "webp"
                "image/svg+xml" -> "svg"
                else -> ""
            }
        }
    }
}
