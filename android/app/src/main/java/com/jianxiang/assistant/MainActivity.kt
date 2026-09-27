package com.jianxiang.assistant

import android.Manifest
import android.app.Activity
import android.content.Context
import android.content.Intent
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
import java.net.HttpURLConnection
import java.net.URL

/**
 * 主界面：启动引擎前台服务 → 自行轮询引擎 HTTP 就绪 → WebView 加载。
 *
 * 刻意不用「服务就绪后广播」的模式：广播是发过即焚的——冷启动没问题，
 * 热启动（Activity 销毁、服务与引擎仍在跑）时新 Activity 永远等不到那条
 * 已经发完的广播，会永远卡在启动页。改为 Activity 直接探测
 * http://127.0.0.1:3080/api/status，冷/热启动走同一条路径，无竞态。
 */
class MainActivity : Activity() {

    private lateinit var web: WebView
    private var loaded = false
    @Volatile private var probing = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        buildWebView()
        showSplash("数字助理启动中…")
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
        probeUntilReady()
    }

    /**
     * 引擎就绪探测：引擎在跑（热启动）则首次探测即过；冷启动等引擎拉起。
     * 探测与 Activity 生命周期解耦：onDestroy 后自动停（线程见 null 即退）。
     */
    private fun probeUntilReady() {
        if (probing) return
        probing = true
        val activity = this
        Thread {
            var waited = 0
            while (probing && waited < 60_000) {
                if (httpOk()) {
                    probing = false
                    activity.runOnUiThread { loadUI() } // loadUI 自带 loaded 幂等
                    return@Thread
                }
                Thread.sleep(200)
                waited += 200
            }
            if (probing) {
                probing = false
                activity.runOnUiThread { showSplash("引擎未就绪——下拉通知栏查看引擎状态，或从桌面重新进入") }
            }
        }.start()
    }

    private fun httpOk(): Boolean {
        var c: HttpURLConnection? = null
        return try {
            c = URL("http://127.0.0.1:${EngineService.PORT}/api/status").openConnection() as HttpURLConnection
            c.connectTimeout = 800
            c.readTimeout = 800
            c.responseCode == 200
        } catch (e: Exception) {
            false
        } finally {
            c?.disconnect()
        }
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
    private fun showSplash(text: String) {
        setContentView(
            FrameLayout(this).apply {
                setBackgroundColor(0xFFFFFFFF.toInt())
                addView(
                    TextView(this@MainActivity).apply {
                        this.text = text
                        textSize = 16f
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

    /** 就绪后换上 WebView；重复探测/重建只加载一次。 */
    private fun loadUI() {
        if (loaded) return
        loaded = true
        setContentView(web)
        web.loadUrl("http://127.0.0.1:${EngineService.PORT}")
    }

    /**
     * 返回键 = 退到后台（moveTaskToBack）而非销毁：引擎在服务里继续跑，
     * 界面与 WebView 状态原样保留，下次进来即热恢复、无需任何等待。
     * 系统内存不足回收后由 onCreate 的探测路径兜底。
     */
    @Deprecated("Deprecated in Java")
    override fun onBackPressed() {
        moveTaskToBack(true)
    }
}
