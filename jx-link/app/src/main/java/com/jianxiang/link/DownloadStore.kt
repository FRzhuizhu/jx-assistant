package com.jianxiang.link

import android.content.Context
import android.net.Uri
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * 手机壳自己的「下载管理」库（首页 → 下载管理）。
 *
 * 为什么要有它：电脑端交付卡的「下载」在网页里是 `blob:` + `a[download]` 那套
 * （远程模式经隧道取 blob 再触发保存）。浏览器里没问题，**WebView 不支持 blob 下载**
 * ——DownloadListener 拿到的是 `blob:https://…` 这种地址，下载器根本取不到内容，
 * 于是手机壳里点交付卡的下载就报"下载失败"（2026-10-27 用户实测）。
 *
 * 现在改成：网页侧把字节（base64 分块）交给壳，壳落到自己的私有目录
 * `files/downloads/`，首页「下载管理」里列出、可打开/分享/删除。这样不依赖
 * 系统 DownloadManager（它也没有 blob 通路），也不需要任何存储权限。
 *
 * 索引存在 `files/downloads/index.json`；进行中的下载只在内存里持有流
 * （分块由同一次 JS 调用链顺序 append，壳被杀就作废，`.part` 下次启动清掉）。
 */
object DownloadStore {

    data class Item(
        val id: String,
        val name: String,
        val mime: String,
        val size: Long,
        val at: Long,
    ) {
        fun sizeText(): String = when {
            size >= 1024L * 1024 -> String.format(Locale.US, "%.1f MB", size / 1048576.0)
            size >= 1024L -> String.format(Locale.US, "%.0f KB", size / 1024.0)
            else -> "$size B"
        }

        fun timeText(): String =
            SimpleDateFormat("MM-dd HH:mm", Locale.getDefault()).format(Date(at))
    }

    private const val DIR = "downloads"
    private const val INDEX = "index.json"

    /** 进行中的下载：id → 输出流（只在内存，见文件头说明） */
    private val open = HashMap<String, FileOutputStream>()
    private val lock = Any()

    fun dir(ctx: Context): File = File(ctx.filesDir, DIR).apply { mkdirs() }

    private fun indexFile(ctx: Context) = File(dir(ctx), INDEX)

    fun list(ctx: Context): List<Item> {
        val f = indexFile(ctx)
        if (!f.exists()) return emptyList()
        return try {
            val arr = JSONArray(f.readText(Charsets.UTF_8))
            (0 until arr.length()).mapNotNull { i ->
                val o = arr.optJSONObject(i) ?: return@mapNotNull null
                val id = o.optString("id")
                if (id.isEmpty()) return@mapNotNull null
                if (!fileOf(ctx, id).exists()) return@mapNotNull null // 文件被外部清掉的不再列出
                Item(
                    id = id,
                    name = o.optString("name", id),
                    mime = o.optString("mime", ""),
                    size = o.optLong("size", 0L),
                    at = o.optLong("at", 0L),
                )
            }.sortedByDescending { it.at }
        } catch (e: Exception) {
            emptyList()
        }
    }

    private fun saveIndex(ctx: Context, items: List<Item>) {
        val arr = JSONArray()
        for (it in items) {
            arr.put(
                JSONObject()
                    .put("id", it.id)
                    .put("name", it.name)
                    .put("mime", it.mime)
                    .put("size", it.size)
                    .put("at", it.at),
            )
        }
        val f = indexFile(ctx)
        val tmp = File(dir(ctx), "$INDEX.tmp")
        tmp.writeText(arr.toString(), Charsets.UTF_8)
        if (f.exists()) f.delete()
        tmp.renameTo(f)
    }

    fun fileOf(ctx: Context, id: String): File {
        // id 全部由本库生成（"<时间戳>-<随机数>[.<扩展名>]"），不含路径分隔符；
        // 这里再挡一层，避免任何来源的 id 变成路径穿越
        val safe = id.replace("/", "_").replace("\\", "_").let { if (it.contains("..")) it.replace("..", "_") else it }
        return File(dir(ctx), safe)
    }

    /** 开始一个下载：返回 id（失败返回 null）。name 只用来定扩展名与展示名。 */
    fun begin(ctx: Context, name: String?, mime: String?): String? {
        val safeName = sanitize(name)
        val ext = extOf(safeName) ?: extOfMime(mime)
        val id = System.currentTimeMillis().toString() + "-" + (1000..9999).random() +
            if (ext.isEmpty()) "" else ".$ext"
        return try {
            val f = fileOf(ctx, id)
            synchronized(lock) {
                open[id] = FileOutputStream(f)
                // 展示名（不带 id）记在内存里：finish 时才写索引
                pending[id] = JSONObject()
                    .put("id", id)
                    .put("name", safeName.ifEmpty { "下载文件" + if (ext.isEmpty()) "" else ".$ext" })
                    .put("mime", mime ?: "")
            }
            id
        } catch (e: Exception) {
            android.util.Log.d("JxLink", "download begin fail: ${e.message}")
            null
        }
    }

    private val pending = HashMap<String, JSONObject>()

    /** 追加一块（base64）。返回 false 表示这块写失败（下载会判失败）。 */
    fun append(id: String?, base64: String?): Boolean {
        if (id == null || base64 == null) return false
        return try {
            val bytes = android.util.Base64.decode(base64, android.util.Base64.DEFAULT)
            synchronized(lock) {
                val os = open[id] ?: return false
                os.write(bytes)
            }
            true
        } catch (e: Exception) {
            android.util.Log.d("JxLink", "download append fail: ${e.message}")
            false
        }
    }

    /** 收尾：关流、按实际大小写索引。返回落成的条目（失败 null）。 */
    fun finish(ctx: Context, id: String?): Item? {
        if (id == null) return null
        var f: File
        val meta: JSONObject
        try {
            synchronized(lock) {
                val os = open.remove(id) ?: return null
                os.flush()
                os.close()
            }
            meta = pending.remove(id) ?: return null
            f = fileOf(ctx, id)
            if (!f.exists()) return null
        } catch (e: Exception) {
            android.util.Log.d("JxLink", "download finish fail: ${e.message}")
            return null
        }
        val item = Item(
            id = id,
            name = meta.optString("name", f.name),
            mime = meta.optString("mime", ""),
            size = f.length(),
            at = System.currentTimeMillis(),
        )
        val items = list(ctx).filter { it.id != id } + item
        saveIndex(ctx, items)
        return item
    }

    fun abort(id: String?) {
        if (id == null) return
        synchronized(lock) {
            try {
                open.remove(id)?.close()
            } catch (e: Exception) {
            }
        }
        pending.remove(id)
    }

    fun delete(ctx: Context, id: String): Boolean {
        abort(id)
        val ok = fileOf(ctx, id).delete()
        saveIndex(ctx, list(ctx).filter { it.id != id })
        return ok
    }

    fun clear(ctx: Context) {
        synchronized(lock) {
            for (os in open.values) try { os.close() } catch (e: Exception) {}
            open.clear()
            pending.clear()
        }
        dir(ctx).listFiles()?.forEach { if (it.name != INDEX) it.delete() }
        saveIndex(ctx, emptyList())
    }

    fun uriFor(ctx: Context, item: Item): Uri =
        androidx.core.content.FileProvider.getUriForFile(
            ctx, ctx.packageName + ".fileprovider", fileOf(ctx, item.id),
        )

    /* ---------- 文件名与类型 ---------- */

    private fun sanitize(name: String?): String {
        val n = (name ?: "").trim().replace('/', '_').replace('\\', '_')
        // 去掉控制字符与 Windows 非法字符（跟电脑端同名保存更省心）
        return n.replace(Regex("[\\u0000-\\u001f:*?\"<>|]"), "_").take(120)
    }

    private fun extOf(name: String?): String? {
        val n = name ?: return null
        val i = n.lastIndexOf('.')
        if (i <= 0 || i == n.length - 1) return null
        val e = n.substring(i + 1).lowercase()
        return if (e.length in 1..5 && e.all { it.isLetterOrDigit() }) e else null
    }

    private fun extOfMime(mime: String?): String {
        val m = (mime ?: "").substringBefore(';').trim().lowercase()
        return when (m) {
            "application/pdf" -> "pdf"
            "application/zip" -> "zip"
            "text/plain" -> "txt"
            "text/csv" -> "csv"
            "text/markdown" -> "md"
            "application/json" -> "json"
            "application/msword" -> "doc"
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document" -> "docx"
            "application/vnd.ms-excel" -> "xls"
            "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" -> "xlsx"
            "application/vnd.ms-powerpoint" -> "ppt"
            "application/vnd.openxmlformats-officedocument.presentationml.presentation" -> "pptx"
            "image/png" -> "png"
            "image/jpeg" -> "jpg"
            "image/gif" -> "gif"
            "audio/mpeg" -> "mp3"
            "video/mp4" -> "mp4"
            else -> ""
        }
    }
}
