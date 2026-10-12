package com.jianxiang.link

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.graphics.Color
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import java.util.concurrent.Executors
import kotlin.math.abs

/**
 * 首页：顶上「扫码连接 / 设置」两行按钮，下面设备列表。
 * - 每行一台电脑：在线/离线（离线整行灰字），点击进入
 * - 长按 → 清空缓存 / 删除（均二次确认）
 * - 冷启动按记忆直达上次所在的电脑页（退出时在哪台、下次就在哪台）
 */
class MainActivity : Activity() {

    private lateinit var devList: LinearLayout
    private lateinit var emptyHint: TextView
    private lateinit var labelDevices: TextView
    private var devs: List<Dev> = emptyList()
    private val online = HashMap<String, Boolean>()   // id -> 在线态（内存，随探测刷新）
    private val main = Handler(Looper.getMainLooper())
    private val io = Executors.newCachedThreadPool()
    private var suppressClear = false                  // 冷启动直达时不清「上次位置」

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        devList = findViewById(R.id.dev_list)
        emptyHint = findViewById(R.id.empty_hint)
        labelDevices = findViewById(R.id.label_devices)
        findViewById<Button>(R.id.btn_scan).setOnClickListener {
            startActivityForResult(Intent(this, ScanActivity::class.java), REQ_SCAN)
        }
        findViewById<Button>(R.id.btn_settings).setOnClickListener {
            startActivity(Intent(this, SettingsActivity::class.java))
        }

        // 记忆位置：上次退出时停留在某台电脑里 → 直接进那台
        val last = DeviceStore.lastDevice(this)
        if (last != null && DeviceStore.byId(this, last) != null) {
            suppressClear = true
            startActivity(Intent(this, DeviceActivity::class.java).putExtra(DeviceActivity.EXTRA_ID, last))
        }
    }

    override fun onResume() {
        super.onResume()
        // 回到首页 = 离开设备页；此后退出应用，下次从首页开始
        if (suppressClear) suppressClear = false
        else DeviceStore.setLastDevice(this, null)
        refresh()
    }

    override fun onDestroy() {
        super.onDestroy()
        io.shutdown()
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == REQ_SCAN && resultCode == RESULT_OK) {
            val entry = data?.getStringExtra("entry") ?: return
            addEntry(entry)
        }
    }

    private fun addEntry(entry: String) {
        val dev = Relay.parseEntry(entry)
        if (dev == null) {
            Toast.makeText(this, "不是有效的连接二维码（应为电脑端 设置·网络访问 的入口码）", Toast.LENGTH_LONG).show()
            return
        }
        if (!DeviceStore.add(this, dev)) {
            Toast.makeText(this, "「${dev.name}」已经添加过了", Toast.LENGTH_SHORT).show()
            refresh()
            return
        }
        Toast.makeText(this, "已添加「${dev.name}」", Toast.LENGTH_SHORT).show()
        refresh()
    }

    /* ---------- 列表与探测 ---------- */

    private fun refresh() {
        devs = DeviceStore.load(this)
        render()
        for (d in devs) {
            io.execute {
                val on = Relay.ping(d)
                main.post {
                    online[d.id] = on
                    if (!isDestroyed) render()
                }
            }
        }
    }

    private fun render() {
        devList.removeAllViews()
        val empty = devs.isEmpty()
        emptyHint.visibility = if (empty) View.VISIBLE else View.GONE
        labelDevices.visibility = if (empty) View.GONE else View.VISIBLE
        for (d in devs) devList.addView(devRow(d))
    }

    private fun dp(v: Float): Int = (v * resources.displayMetrics.density + 0.5f).toInt()

    private fun devRow(d: Dev): View {
        val on = online[d.id]
        val ctx = this
        val row = LinearLayout(ctx).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setBackgroundResource(R.drawable.bg_dev_row)
            setPadding(dp(14f), dp(12f), dp(12f), dp(12f))
            layoutParams = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT,
            ).apply { bottomMargin = dp(10f) }
        }

        val icon = ImageView(ctx).apply {
            setImageResource(R.drawable.ic_monitor)
            if (on == false) setColorFilter(Color.parseColor("#9AA0A6"))
        }
        row.addView(
            icon, LinearLayout.LayoutParams(dp(24f), dp(24f)).apply { marginEnd = dp(12f) },
        )

        val mid = LinearLayout(ctx).apply { orientation = LinearLayout.VERTICAL }
        mid.addView(
            TextView(ctx).apply {
                text = d.name
                textSize = 15f
                setTypeface(typeface, android.graphics.Typeface.BOLD)
                setTextColor(if (on == false) Color.parseColor("#9AA0A6") else Color.parseColor("#1F2329"))
            },
        )
        mid.addView(
            TextView(ctx).apply {
                textSize = 12f
                when (on) {
                    null -> { text = "检测中…"; setTextColor(Color.parseColor("#8A909C")) }
                    true -> { text = "在线"; setTextColor(Color.parseColor("#2EA043")) }
                    false -> { text = "离线"; setTextColor(Color.parseColor("#9AA0A6")) }
                }
            },
        )
        row.addView(
            mid, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f),
        )

        row.addView(
            ImageView(ctx).apply {
                setImageResource(R.drawable.ic_chevron)
                if (on == false) alpha = 0.5f
            },
        )

        row.setOnClickListener { openDevice(d.id) }
        row.setOnLongClickListener {
            longPressMenu(d)
            true
        }
        return row
    }

    private fun openDevice(id: String) {
        startActivity(Intent(this, DeviceActivity::class.java).putExtra(DeviceActivity.EXTRA_ID, id))
    }

    /* ---------- 长按：清空缓存 / 删除（各带二次确认） ---------- */

    private fun longPressMenu(d: Dev) {
        AlertDialog.Builder(this)
            .setTitle(d.name)
            .setItems(arrayOf("清空缓存", "删除")) { _, which ->
                when (which) {
                    0 -> confirmClearCache(d)
                    1 -> confirmDelete(d)
                }
            }
            .show()
    }

    private fun confirmClearCache(d: Dev) {
        AlertDialog.Builder(this)
            .setTitle("清空缓存")
            .setMessage("确定清空「${d.name}」在本机保存的全部缓存与页面数据？\n\n不影响电脑端数据。")
            .setPositiveButton("清空") { _, _ ->
                io.execute {
                    val ok = Cache.clearDevice(this, d)
                    main.post {
                        Toast.makeText(
                            this, if (ok) "已清空「${d.name}」的缓存" else "清空未完全成功（页面可能仍开着，稍后再试）", Toast.LENGTH_SHORT,
                        ).show()
                    }
                }
            }
            .setNegativeButton("取消", null)
            .show()
    }

    private fun confirmDelete(d: Dev) {
        AlertDialog.Builder(this)
            .setTitle("删除")
            .setMessage("确定删除「${d.name}」？\n\n删除后需要重新扫码才能再连接，其缓存也会一并清除。")
            .setPositiveButton("删除") { _, _ ->
                io.execute {
                    Cache.clearDevice(this, d)
                    DeviceStore.remove(this, d.id)
                    main.post {
                        online.remove(d.id)
                        Toast.makeText(this, "已删除「${d.name}」", Toast.LENGTH_SHORT).show()
                        refresh()
                    }
                }
            }
            .setNegativeButton("取消", null)
            .show()
    }

    companion object {
        private const val REQ_SCAN = 1
    }
}
