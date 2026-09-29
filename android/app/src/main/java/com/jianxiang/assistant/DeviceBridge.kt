package com.jianxiang.assistant

import android.util.Log
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.atomic.AtomicBoolean

/**
 * 设备桥：Kotlin 壳 ↔ 引擎的命令通道。方向是「壳轮询引擎」——长轮询
 * GET /api/device/poll?svc=0/1（svc 上报无障碍服务连接状态），拿到命令执行完
 * POST /api/device/result 回传。引擎侧队列与超时见 device_bridge.go。
 *
 * 引擎未起时静默重试（500ms~1.5s 退避）；回传放独立线程，截屏 base64
 * 编码量大也不堵轮询循环。
 */
object DeviceBridge {
    private const val TAG = "JXDevice"
    private val running = AtomicBoolean(false)

    fun start() {
        if (!running.compareAndSet(false, true)) return
        Thread {
            Log.i(TAG, "设备桥启动")
            while (running.get()) {
                try {
                    val svc = if (DeviceAccessibilityService.connected) 1 else 0
                    val conn = URL("http://127.0.0.1:${EngineService.PORT}/api/device/poll?svc=$svc")
                        .openConnection() as HttpURLConnection
                    conn.connectTimeout = 5000
                    conn.readTimeout = 25000
                    val code = conn.responseCode
                    val body = if (code == 200) conn.inputStream.bufferedReader().readText() else null
                    conn.disconnect()
                    when {
                        code == 204 -> Unit // 无命令，回环继续长轮询
                        code == 200 && body != null -> {
                            handle(JSONObject(body))
                            continue // 立即取下一条，不睡
                        }
                        else -> Thread.sleep(1000)
                    }
                } catch (e: Exception) {
                    Thread.sleep(1500)
                }
            }
            Log.i(TAG, "设备桥退出")
        }.apply {
            name = "DeviceBridge"
            isDaemon = true
        }.start()
    }

    fun stop() { running.set(false) }

    private fun handle(cmd: JSONObject) {
        val id = cmd.optString("id")
        val op = cmd.optString("op")
        val args = cmd.optJSONObject("args")
        val r = JSONObject()
        try {
            exec(op, args, r)
        } catch (e: Exception) {
            r.put("ok", false).put("err", "" + e.message)
        }
        val payload = JSONObject().put("id", id).put("result", r)
        Thread { post(payload) }.apply { isDaemon = true }.start()
    }

    private fun exec(op: String, args: JSONObject?, r: JSONObject) {
        val svc = DeviceAccessibilityService.svcInst ?: run {
            r.put("ok", false).put("err", "无障碍服务未连接：请开启 设置→无障碍→已下载的应用→数字助理")
            return
        }
        when (op) {
            "ping" -> r.put("ok", true)
            "screenshot" -> {
                // 回调式 API：本线程同步等待（handle 已把回传放独立线程，不堵轮询）
                val latch = java.util.concurrent.CountDownLatch(1)
                var out: JSONObject? = null
                svc.screenshot { ok, b64, w, h, err ->
                    out = if (ok) JSONObject().put("ok", true).put("data", b64).put("w", w).put("h", h)
                    else JSONObject().put("ok", false).put("err", err ?: "未知错误")
                    latch.countDown()
                }
                latch.await(15, java.util.concurrent.TimeUnit.SECONDS)
                val o = out
                if (o != null) for (k in o.keys()) r.put(k, o.get(k))
                else r.put("ok", false).put("err", "截屏超时")
            }
            "ui_dump" -> r.put("ok", true).put("data", svc.uiDump())
            "tap" -> {
                val x = args?.optDouble("x") ?: 0.0
                val y = args?.optDouble("y") ?: 0.0
                val latch = java.util.concurrent.CountDownLatch(1)
                var err: String? = null
                svc.tap(x.toFloat(), y.toFloat()) { ok, e -> if (!ok) err = e; latch.countDown() }
                latch.await(8, java.util.concurrent.TimeUnit.SECONDS)
                r.put("ok", err == null).put("err", err ?: JSONObject.NULL)
            }
            "swipe" -> {
                val latch = java.util.concurrent.CountDownLatch(1)
                var err: String? = null
                svc.swipe(
                    (args?.optDouble("x1") ?: 0.0).toFloat(), (args?.optDouble("y1") ?: 0.0).toFloat(),
                    (args?.optDouble("x2") ?: 0.0).toFloat(), (args?.optDouble("y2") ?: 0.0).toFloat(),
                    (args?.optLong("duration") ?: 300L)
                ) { ok, e -> if (!ok) err = e; latch.countDown() }
                latch.await(8, java.util.concurrent.TimeUnit.SECONDS)
                r.put("ok", err == null).put("err", err ?: JSONObject.NULL)
            }
            "type" -> {
                val err = svc.type(args?.optString("text") ?: "")
                r.put("ok", err == null).put("err", err ?: JSONObject.NULL)
            }
            "key" -> {
                val err = svc.key(args?.optString("key") ?: "")
                r.put("ok", err == null).put("err", err ?: JSONObject.NULL)
            }
            else -> r.put("ok", false).put("err", "未知命令：$op")
        }
    }

    private fun post(body: JSONObject) {
        repeat(3) { attempt ->
            try {
                val conn = URL("http://127.0.0.1:${EngineService.PORT}/api/device/result")
                    .openConnection() as HttpURLConnection
                conn.requestMethod = "POST"
                conn.doOutput = true
                conn.connectTimeout = 5000
                conn.readTimeout = 5000
                conn.setRequestProperty("Content-Type", "application/json")
                conn.outputStream.use { it.write(body.toString().toByteArray()) }
                val ok = conn.responseCode in 200..299
                conn.disconnect()
                if (ok) return
            } catch (e: Exception) {
                Log.w(TAG, "设备命令结果回传失败(第${attempt + 1}次): " + e.message)
            }
            Thread.sleep(500)
        }
    }
}
