package com.jianxiang.link

import android.app.Activity
import android.app.AlertDialog
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.widget.Button
import android.widget.EditText
import android.widget.Toast
import java.util.concurrent.Executors

/**
 * 设置（单页两区域）：
 * 1. 清空应用缓存 —— 所有电脑的独立 Profile + 默认域（HTTP 缓存/localStorage）+ 应用缓存
 * 2. 反馈意见 —— 标题+正文，封 multipart 帧经加密中继发 POST /api/feedback；
 *    依次尝试（上次所在电脑优先），第一台成功的为准
 */
class SettingsActivity : Activity() {

    private val main = Handler(Looper.getMainLooper())
    private val io = Executors.newSingleThreadExecutor()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_settings)

        findViewById<Button>(R.id.btn_clear_cache).setOnClickListener { confirmClear() }
        findViewById<Button>(R.id.btn_feedback).setOnClickListener { sendFeedback() }
    }

    override fun onDestroy() {
        super.onDestroy()
        io.shutdown()
    }

    private fun confirmClear() {
        AlertDialog.Builder(this)
            .setTitle("清空应用缓存")
            .setMessage("确定清空所有电脑在本机的连接缓存与应用缓存？\n\n设备列表会保留；不影响电脑端数据。")
            .setPositiveButton("清空") { _, _ ->
                io.execute {
                    val ok = Cache.clearAll(this)
                    main.post {
                        Toast.makeText(
                            this,
                            if (ok) "已清空应用缓存" else "部分缓存未能清除（有页面可能仍开着）",
                            Toast.LENGTH_SHORT,
                        ).show()
                    }
                }
            }
            .setNegativeButton("取消", null)
            .show()
    }

    private fun sendFeedback() {
        val title = findViewById<EditText>(R.id.fb_title).text.toString().trim()
        val content = findViewById<EditText>(R.id.fb_content).text.toString().trim()
        if (title.isEmpty()) {
            Toast.makeText(this, "请填写标题", Toast.LENGTH_SHORT).show()
            return
        }
        if (content.isEmpty()) {
            Toast.makeText(this, "请填写正文", Toast.LENGTH_SHORT).show()
            return
        }
        val btn = findViewById<Button>(R.id.btn_feedback)
        btn.isEnabled = false
        btn.text = "发送中…"
        val ver = try {
            packageManager.getPackageInfo(packageName, 0).versionName ?: "?"
        } catch (e: Exception) {
            "?"
        }
        io.execute {
            // 上次所在的电脑优先，其余依次；第一台成功即止
            val last = DeviceStore.lastDevice(this)
            val devs = DeviceStore.load(this).sortedByDescending { it.id == last }
            var doneOn: Dev? = null
            var err = "没有可用的电脑——先扫码连接一台并保持其在线"
            for (d in devs) {
                when (val r = Relay.sendFeedback(d, title, content, ver)) {
                    is Relay.SendResult.Ok -> {
                        doneOn = d
                        break
                    }
                    is Relay.SendResult.Err -> err = r.msg
                }
            }
            val doneDev = doneOn
            main.post {
                if (isDestroyed) return@post
                btn.isEnabled = true
                btn.text = "发送反馈"
                if (doneDev != null) {
                    findViewById<EditText>(R.id.fb_title).setText("")
                    findViewById<EditText>(R.id.fb_content).setText("")
                    Toast.makeText(this, "已发送 ✓（经「${doneDev.name}」）", Toast.LENGTH_SHORT).show()
                } else {
                    Toast.makeText(this, "发送失败：$err", Toast.LENGTH_LONG).show()
                }
            }
        }
    }
}
