package com.jianxiang.assistant

import android.Manifest
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.view.ViewGroup
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat

/**
 * 主界面：启动引擎前台服务 → 等「引擎就绪」广播 → WebView 全屏加载
 * http://127.0.0.1:3080。通知权限仅影响「AI 任务完成提醒」类能力，
 * 首启引导一次，拒绝不影响主功能。
 */
class MainActivity : AppCompatActivity() {

    private lateinit var web: WebView
    private var loaded = false

    private val notifPerm =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            // 拒绝也继续：通知只影响完成提醒，引擎照常跑
            startEngine()
        }

    private val readyReceiver = object : BroadcastReceiver() {
        override fun onReceive(ctx: Context, intent: Intent) {
            if (intent.getBooleanExtra("ok", false)) loadUI()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // 先请求通知权限（13+ 需运行时申请），回调里再拉引擎
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
            != PackageManager.PERMISSION_GRANTED
        ) {
            notifPerm.launch(Manifest.permission.POST_NOTIFICATIONS)
        } else {
            startEngine()
        }
        registerReceiver(readyReceiver, IntentFilter(EngineService.BROADCAST_READY))
        buildWebView()
        showSplash()
    }

    private fun startEngine() {
        ContextCompat.startForegroundService(this, Intent(this, EngineService::class.java))
    }

    private fun buildWebView() {
        web = WebView(this)
        web.layoutParams = ViewGroup.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT
        )
        web.settings.javaScriptEnabled = true
        web.settings.domStorageEnabled = true
        // SSE/长连接不被 WebView 缓存拦截；同源仅本地引擎
        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(v: WebView?, r: WebResourceRequest?): Boolean {
                val u = r?.url ?: return false
                // 只放行本机引擎与引擎自身的链接跳转；外部链接交给系统浏览器
                if (u.host == "127.0.0.1") return false
                return try {
                    startActivity(Intent(Intent.ACTION_VIEW, u)); true
                } catch (e: Exception) { true }
            }
        }
    }

    /** 引擎就绪前的极简启动页（应用自带，不联网）。 */
    private fun showSplash() {
        setContentView(
            android.widget.FrameLayout(this).apply {
                setBackgroundColor(0xFFFFFFFF.toInt())
                addView(
                    android.widget.TextView(this@MainActivity).apply {
                        text = "数字助理启动中…"
                        textSize = 18f
                        setTextColor(0xFF1A56DB.toInt())
                        gravity = android.view.Gravity.CENTER
                    },
                    ViewGroup.LayoutParams(
                        ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT
                    )
                )
            }
        )
    }

    /** 就绪后换上 WebView；重复广播/重建只加载一次。 */
    private fun loadUI() {
        if (loaded) return
        loaded = true
        setContentView(web)
        web.loadUrl("http://127.0.0.1:${EngineService.PORT}")
    }

    /** 返回键：网页能后退就后退，不直接退出（引擎仍在服务里跑）。 */
    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        if (this::web.isInitialized && web.canGoBack()) web.goBack() else @Suppress("DEPRECATION") super.onBackPressed()
    }

    override fun onDestroy() {
        unregisterReceiver(readyReceiver)
        super.onDestroy()
    }
}
