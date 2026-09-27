package com.jianxiang.assistant

import android.Manifest
import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.view.Gravity
import android.view.ViewGroup
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.TextView
import androidx.core.content.ContextCompat

/**
 * 主界面：启动引擎前台服务 → 等「引擎就绪」广播 → WebView 全屏加载
 * http://127.0.0.1:3080。通知权限仅影响完成提醒类能力，首启引导一次，
 * 拒绝不影响主功能。刻意不依赖 appcompat/activity-ktx——薄壳用平台
 * 原生 API（Activity + startActivityForResult 式权限回调）即可。
 */
class MainActivity : Activity() {

    private lateinit var web: WebView
    private var loaded = false

    private val readyReceiver = object : BroadcastReceiver() {
        override fun onReceive(ctx: Context, intent: Intent) {
            if (intent.getBooleanExtra("ok", false)) loadUI()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        registerReceiver(
            readyReceiver,
            IntentFilter(EngineService.BROADCAST_READY),
            Context.RECEIVER_NOT_EXPORTED
        )
        buildWebView()
        showSplash()
        // 通知权限（13+ 运行时申请）：拒绝也继续，通知只影响完成提醒
        if (Build.VERSION.SDK_INT >= 33 &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
            != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        } else {
            startEngine()
        }
    }

    override fun onRequestPermissionsResult(
        requestCode: Int, permissions: Array<out String>, grantResults: IntArray
    ) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == 1) startEngine()
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
        web.webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(v: WebView?, r: WebResourceRequest?): Boolean {
                val u = r?.url ?: return false
                // 本机引擎自处理；外部链接交给系统浏览器
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
            FrameLayout(this).apply {
                setBackgroundColor(0xFFFFFFFF.toInt())
                addView(
                    TextView(this@MainActivity).apply {
                        text = "数字助理启动中…"
                        textSize = 18f
                        setTextColor(0xFF1A56DB.toInt())
                        gravity = Gravity.CENTER
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
        if (this::web.isInitialized && web.canGoBack()) web.goBack()
        else @Suppress("DEPRECATION") super.onBackPressed()
    }

    override fun onDestroy() {
        unregisterReceiver(readyReceiver)
        super.onDestroy()
    }
}
