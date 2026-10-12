package com.jianxiang.link

import org.json.JSONObject
import java.io.OutputStream
import java.net.HttpURLConnection
import java.net.URI
import java.net.URL
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * 与 210 网关的中继协议客户端（和 gate.html / remote-boot.js 同一套帧协议）：
 * - 封包 AES-256-GCM（key = SHA-256(pairKey)，12B IV，尾 16B tag），网关只见密文
 * - 在线探测：POST /relay 发一个极小的 GET /api/shell-hash 帧——网关对不在线的
 *   设备回 503；电脑在线则 200 + 密文帧（不关心内容，状态码即在线态）
 * - 反馈发送：multipart 表单封帧 POST /api/feedback（电脑端 handleFeedbackCreate）
 *
 * 全部方法阻塞，调用方上执行器线程。
 */
object Relay {
    private val rng = SecureRandom()

    sealed class SendResult {
        object Ok : SendResult()
        data class Err(val msg: String) : SendResult()
    }

    /* ---------- 入口二维码解析 ---------- */

    /** 解析扫码文本 → Dev；不是有效入口返回 null */
    fun parseEntry(textIn: String): Dev? {
        val t = textIn.trim()
        if (!t.startsWith("http://") && !t.startsWith("https://")) return null
        return try {
            val u = URI(t)
            val scheme = u.scheme ?: "http"
            val host = u.host ?: return null
            val port = if (u.port > 0) u.port else (if (scheme == "https") 443 else 80)
            val path = u.path ?: ""
            val m = Regex("^/a/([^/]+)$").find(path)
            if (m != null) {
                // 远程入口：http://网关:端口/a/设备号#pk=配对密钥
                val frag = u.fragment ?: ""
                val pk = Regex("(?:^|&)pk=([^&]+)").find(frag)?.groupValues?.get(1) ?: return null
                val id = m.groupValues[1]
                Dev(
                    id = id, name = id, kind = "relay", scheme = scheme,
                    host = host, port = port, pairKey = pk, url = "", addedAt = System.currentTimeMillis(),
                )
            } else {
                // 局域网直连：普通 http(s)://host:port（归一到根路径）
                val lanUrl = "$scheme://$host" + (if (u.port > 0) ":$port" else "") + "/"
                Dev(
                    id = "$host:$port", name = host, kind = "lan", scheme = scheme,
                    host = host, port = port, pairKey = "", url = lanUrl,
                    addedAt = System.currentTimeMillis(),
                )
            }
        } catch (e: Exception) {
            null
        }
    }

    /* ---------- AES-256-GCM 封包（与服务端 sealJSON/openPack 一致） ---------- */

    private fun keyBytes(pairKey: String): ByteArray =
        MessageDigest.getInstance("SHA-256").digest(pairKey.toByteArray(Charsets.UTF_8))

    private fun seal(pairKey: String, payload: String): String {
        val key = keyBytes(pairKey)
        val iv = ByteArray(12).also { rng.nextBytes(it) }
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, iv))
        val ct = c.doFinal(payload.toByteArray(Charsets.UTF_8))
        return JSONObject()
            .put("iv", Base64.getEncoder().encodeToString(iv))
            .put("ct", Base64.getEncoder().encodeToString(ct))
            .toString()
    }

    private fun open(pairKey: String, ivB64: String, ctB64: String): String? {
        return try {
            val iv = Base64.getDecoder().decode(ivB64)
            val ct = Base64.getDecoder().decode(ctB64)
            val c = Cipher.getInstance("AES/GCM/NoPadding")
            c.init(Cipher.DECRYPT_MODE, SecretKeySpec(keyBytes(pairKey), "AES"), GCMParameterSpec(128, iv))
            String(c.doFinal(ct), Charsets.UTF_8)
        } catch (e: Exception) {
            null
        }
    }

    /* ---------- 在线探测 ---------- */

    /** true=在线。relay：网关 200 且有密文帧；lan：入口可访问。 */
    fun ping(dev: Dev, timeoutMs: Int = 5000): Boolean = try {
        if (dev.kind == "lan") pingLan(dev, timeoutMs) else pingRelay(dev, timeoutMs)
    } catch (e: Exception) {
        false
    }

    private fun pingRelay(dev: Dev, timeoutMs: Int): Boolean {
        val frame = JSONObject()
            .put("t", "req").put("id", "ping1").put("method", "GET")
            .put("path", "/api/shell-hash").put("headers", JSONObject())
        val payload = JSONObject()
            .put("pk", dev.pairKey)
            .put("ts", System.currentTimeMillis() / 1000)
            .put("frame", frame)
        val pack = seal(dev.pairKey, payload.toString())
        val conn = openConn("${dev.scheme}://${dev.host}:${dev.port}/relay", "POST", timeoutMs)
        conn.setRequestProperty("X-Relay-Peer", dev.id)
        writeBody(conn, pack.toByteArray(Charsets.UTF_8), "application/json; charset=utf-8")
        if (conn.responseCode != 200) return false
        // 读到首行密文帧即证明电脑在网关上活着（网关只转发，伪造不出密文）
        val line = conn.inputStream.bufferedReader().readLine() ?: return false
        return line.contains("\"iv\"")
    }

    private fun pingLan(dev: Dev, timeoutMs: Int): Boolean {
        val conn = openConn(dev.url, "GET", timeoutMs)
        conn.instanceFollowRedirects = true
        return conn.responseCode in 200..399
    }

    /* ---------- 反馈发送（电脑端 POST /api/feedback，multipart） ---------- */

    fun sendFeedback(dev: Dev, title: String, content: String, ver: String): SendResult {
        val boundary = "----jxlink" + System.currentTimeMillis().toString(36)
        val fields = listOf(
            "kind" to "other",
            "title" to title,
            "content" to content,
            "sessionId" to "",
            "diagnostics" to "{\"src\":\"jx-link\",\"ver\":\"$ver\"}",
        )
        val body = StringBuilder()
        for ((k, v) in fields) {
            body.append("--").append(boundary).append("\r\n")
                .append("Content-Disposition: form-data; name=\"").append(k).append("\"\r\n\r\n")
                .append(v).append("\r\n")
        }
        body.append("--").append(boundary).append("--\r\n")

        if (dev.kind == "lan") {
            // 局域网：直接 multipart POST（无加密层）
            val conn = openConn(dev.url + "api/feedback", "POST", 8000)
            writeBody(
                conn, body.toString().toByteArray(Charsets.UTF_8),
                "multipart/form-data; boundary=$boundary",
            )
            return if (conn.responseCode in 200..299) SendResult.Ok
            else SendResult.Err("HTTP ${conn.responseCode}")
        }

        // 中继：表单封帧走加密隧道
        val frame = JSONObject()
            .put("t", "req").put("id", "fb1").put("method", "POST").put("path", "/api/feedback")
            .put(
                "headers",
                JSONObject().put("content-type", "multipart/form-data; boundary=$boundary"),
            )
            .put("body", Base64.getEncoder().encodeToString(body.toString().toByteArray(Charsets.UTF_8)))
        val payload = JSONObject()
            .put("pk", dev.pairKey)
            .put("ts", System.currentTimeMillis() / 1000)
            .put("frame", frame)
        val pack = seal(dev.pairKey, payload.toString())

        val conn = openConn("${dev.scheme}://${dev.host}:${dev.port}/relay", "POST", 10000)
        conn.setRequestProperty("X-Relay-Peer", dev.id)
        writeBody(conn, pack.toByteArray(Charsets.UTF_8), "application/json; charset=utf-8")
        if (conn.responseCode != 200) {
            val msg = try {
                JSONObject(conn.errorStream?.bufferedReader()?.readText() ?: "").optString("error", "")
            } catch (e: Exception) {
                ""
            }
            return SendResult.Err(if (msg.isEmpty()) "网关 HTTP ${conn.responseCode}" else msg)
        }
        // NDJSON 逐帧解密：head 2xx=成功；err/authfail=失败
        val reader = conn.inputStream.bufferedReader()
        while (true) {
            val line = reader.readLine() ?: break
            val f = try {
                JSONObject(line)
            } catch (e: Exception) {
                continue
            }
            if (!f.has("iv") || !f.has("ct")) continue
            val plain = open(dev.pairKey, f.getString("iv"), f.getString("ct")) ?: continue
            val fr = try {
                JSONObject(plain)
            } catch (e: Exception) {
                continue
            }
            when (fr.optString("t")) {
                "head" -> return if (fr.optInt("status", 0) in 200..299) SendResult.Ok
                else SendResult.Err("电脑端 HTTP ${fr.optInt("status")}")

                "err" -> return SendResult.Err(fr.optString("msg", "远程错误"))
                "authfail" -> return SendResult.Err("配对密钥错误")
            }
        }
        return SendResult.Err("无响应（电脑可能离线）")
    }

    /* ---------- HTTP 底座 ---------- */

    private fun openConn(url: String, method: String, timeoutMs: Int): HttpURLConnection {
        val conn = URL(url).openConnection() as HttpURLConnection
        conn.requestMethod = method
        conn.connectTimeout = timeoutMs
        conn.readTimeout = timeoutMs + 2000
        conn.instanceFollowRedirects = false
        conn.setRequestProperty("User-Agent", "jx-link/0.1 (Android)")
        return conn
    }

    private fun writeBody(conn: HttpURLConnection, body: ByteArray, contentType: String) {
        conn.doOutput = true
        conn.setFixedLengthStreamingMode(body.size)
        conn.setRequestProperty("Content-Type", contentType)
        val out: OutputStream = conn.outputStream
        out.write(body)
        out.flush()
        out.close()
    }
}
