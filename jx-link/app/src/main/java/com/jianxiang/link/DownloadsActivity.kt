package com.jianxiang.link

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.graphics.Color
import android.net.Uri
import android.os.Bundle
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast

/**
 * 首页 →「下载管理」：手机壳自己下过的文件（交付卡「下载」、导出之类）。
 *
 * 交付卡在网页里是 blob 下载，WebView 接不住（详见 DownloadStore 文件头），
 * 所以这些文件由壳落到 `files/downloads/` 后统一在这里管理：点开看、分享给
 * 别的 App、删除、清空。打开走 FileProvider（私有目录不给 file:// 外露）。
 */
class DownloadsActivity : Activity() {

    private lateinit var list: LinearLayout
    private lateinit var emptyHint: TextView
    private lateinit var title: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_downloads)
        list = findViewById(R.id.dl_list)
        emptyHint = findViewById(R.id.dl_empty)
        title = findViewById(R.id.dl_title)
        findViewById<Button>(R.id.dl_back).setOnClickListener { finish() }
        findViewById<Button>(R.id.dl_clear).setOnClickListener { confirmClear() }
    }

    override fun onResume() {
        super.onResume()
        render()
    }

    private fun dp(v: Float): Int = (v * resources.displayMetrics.density + 0.5f).toInt()

    private fun render() {
        val items = DownloadStore.list(this)
        title.text = if (items.isEmpty()) "下载管理" else "下载管理（${items.size}）"
        list.removeAllViews()
        emptyHint.visibility = if (items.isEmpty()) View.VISIBLE else View.GONE
        for (item in items) list.addView(row(item))
    }

    private fun row(item: DownloadStore.Item): View {
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
        row.addView(
            ImageView(ctx).apply { setImageResource(R.drawable.ic_download) },
            LinearLayout.LayoutParams(dp(24f), dp(24f)).apply { marginEnd = dp(12f) },
        )
        val mid = LinearLayout(ctx).apply { orientation = LinearLayout.VERTICAL }
        mid.addView(
            TextView(ctx).apply {
                text = item.name
                textSize = 15f
                setTypeface(typeface, android.graphics.Typeface.BOLD)
                setTextColor(Color.parseColor("#1F2329"))
                maxLines = 2
            },
        )
        mid.addView(
            TextView(ctx).apply {
                text = "${item.sizeText()} · ${item.timeText()}"
                textSize = 12f
                setTextColor(Color.parseColor("#8A909C"))
            },
        )
        row.addView(mid, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        row.setOnClickListener { open(item) }
        row.setOnLongClickListener {
            AlertDialog.Builder(this)
                .setTitle(item.name)
                .setItems(arrayOf("打开", "分享", "删除")) { _, which ->
                    when (which) {
                        0 -> open(item)
                        1 -> share(item)
                        2 -> confirmDelete(item)
                    }
                }
                .show()
            true
        }
        return row
    }

    private fun open(it: DownloadStore.Item) {
        try {
            val uri: Uri = DownloadStore.uriFor(this, it)
            val mime = if (it.mime.isBlank()) "*/*" else it.mime
            val i = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(uri, mime)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            startActivity(Intent.createChooser(i, "打开方式"))
        } catch (e: Exception) {
            Toast.makeText(this, "这台手机上打不开这个文件：${e.message}", Toast.LENGTH_LONG).show()
        }
    }

    private fun share(it: DownloadStore.Item) {
        try {
            val uri: Uri = DownloadStore.uriFor(this, it)
            val i = Intent(Intent.ACTION_SEND).apply {
                type = if (it.mime.isBlank()) "*/*" else it.mime
                putExtra(Intent.EXTRA_STREAM, uri)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            startActivity(Intent.createChooser(i, "分享到"))
        } catch (e: Exception) {
            Toast.makeText(this, "分享失败：${e.message}", Toast.LENGTH_LONG).show()
        }
    }

    private fun confirmDelete(it: DownloadStore.Item) {
        AlertDialog.Builder(this)
            .setTitle("删除")
            .setMessage("确定删除「${it.name}」？")
            .setPositiveButton("删除") { _, _ ->
                DownloadStore.delete(this, it.id)
                render()
            }
            .setNegativeButton("取消", null)
            .show()
    }

    private fun confirmClear() {
        if (DownloadStore.list(this).isEmpty()) {
            Toast.makeText(this, "还没有下载的文件", Toast.LENGTH_SHORT).show()
            return
        }
        AlertDialog.Builder(this)
            .setTitle("清空下载")
            .setMessage("确定删除本机下载的全部文件？\n\n只删手机上的副本，电脑端文件不受影响。")
            .setPositiveButton("清空") { _, _ ->
                DownloadStore.clear(this)
                render()
            }
            .setNegativeButton("取消", null)
            .show()
    }
}
