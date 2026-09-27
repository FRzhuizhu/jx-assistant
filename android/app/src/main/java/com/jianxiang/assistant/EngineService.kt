package com.jianxiang.assistant

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

/**
 * 引擎前台服务：拉起并看护 Go 引擎进程（libjx.so，位于
 * nativeLibraryDir——targetSdk 29+ 下唯一可执行自带二进制的位置），
 * 锁屏/挂后台继续运行（dataSync 型 + START_STICKY）。
 * 通知栏常驻，带「停止」动作；引擎 HTTP 就绪探测通过后广播给 Activity。
 */
class EngineService : Service() {

    companion object {
        const val PORT = 3080
        const val CHANNEL_ID = "engine"
        const val NOTIFY_ID = 1
        const val ACTION_STOP = "com.jianxiang.assistant.STOP_ENGINE"
        const val BROADCAST_READY = "com.jianxiang.assistant.ENGINE_READY"

        /** 引擎数据根目录（config/data/技能全在这下面，随应用卸载清除）。 */
        fun engineRoot(ctx: Context): File = File(ctx.filesDir, "engine")

        fun libPath(ctx: Context): File {
            // jniLibs 里以 libjx.so 命名，系统释放到 nativeLibraryDir
            return File(ctx.applicationInfo.nativeLibraryDir, "libjx.so")
        }
    }

    private var proc: Process? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        startForegroundWithType()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            stopEngine()
            stopSelf()
            return START_NOT_STICKY
        }
        startEngineIfNeeded()
        return START_STICKY
    }

    private fun startForegroundWithType() {
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= 26) {
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "数字助理引擎", NotificationManager.IMPORTANCE_LOW)
            )
        }
        val pi = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        val stopPi = PendingIntent.getService(
            this, 1, Intent(this, EngineService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
        )
        val n = if (Build.VERSION.SDK_INT >= 26) {
            Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("数字助理运行中")
                .setContentText("智能体引擎已在本地就绪，点按打开")
                .setSmallIcon(android.R.drawable.stat_notify_sync)
                .setContentIntent(pi)
                .setOngoing(true)
                .addAction(0, "停止", stopPi)
                .build()
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
                .setContentTitle("数字助理运行中")
                .setContentText("智能体引擎已在本地就绪，点按打开")
                .setSmallIcon(android.R.drawable.stat_notify_sync)
                .setContentIntent(pi)
                .setOngoing(true)
                .build()
        }
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(NOTIFY_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            startForeground(NOTIFY_ID, n)
        }
    }

    /** 引擎进程 + 看护协程：进程死亡后自动重启（服务活着就保引擎活着）。 */
    private fun startEngineIfNeeded() {
        if (proc?.isAlive == true) return
        val root = engineRoot(this).apply {
            mkdirs()
            // 数据根只归本应用 uid，权限最小化
            setWritable(true, false)
        }
        val lib = libPath(this)
        if (!lib.exists() || !lib.canExecute()) {
            broadcastReady(false)
            return
        }
        // config.json 不预置：首次启动引擎会用默认配置（mock 模式 + 空工作区），
        // 用户在设置页录入 API Key 后自动落盘（引擎 Config.Save 已 0600）
        val args = listOf(
            lib.absolutePath,
            "-noopen",
            "-port", PORT.toString(),
            "-config", File(root, "config.json").absolutePath
        )
        try {
            val pb = ProcessBuilder(args).apply {
                directory(root)
                environment()["HOME"] = root.absolutePath
                environment()["JX_WORKSPACE_DEFAULT"] = root.absolutePath
                redirectErrorStream(true)
            }
            proc = pb.start()
            // 吞掉引擎 stdout（避免管道写满阻塞引擎），进程结束后触发守护重启
            val p = proc!!
            Thread {
                try {
                    p.inputStream.use { it.readBytes() }
                } catch (_: Exception) {}
                if (proc === p && p.isAlive.not()) {
                    // 非正常退出且服务仍在：1 秒后拉起
                    Thread.sleep(1000)
                    if (proc === p) {
                        proc = null
                        startEngineIfNeeded()
                    }
                }
            }.start()
        } catch (e: Exception) {
            broadcastReady(false)
            return
        }
        // 就绪探测：HTTP 200 即广播（WebView 收到广播后才加载，避免白屏）
        Thread {
            for (i in 1..100) {
                if (proc?.isAlive != true) return@Thread
                try {
                    val c = URL("http://127.0.0.1:$PORT/api/status").openConnection() as HttpURLConnection
                    c.connectTimeout = 1500
                    c.readTimeout = 1500
                    if (c.responseCode == 200) {
                        c.disconnect()
                        broadcastReady(true)
                        return@Thread
                    }
                    c.disconnect()
                } catch (_: Exception) {}
                Thread.sleep(300)
            }
        }.start()
    }

    private fun stopEngine() {
        proc?.destroy()
        proc = null
    }

    private fun broadcastReady(ok: Boolean) {
        sendBroadcast(Intent(BROADCAST_READY).setPackage(packageName).putExtra("ok", ok))
    }

    override fun onDestroy() {
        stopEngine()
        super.onDestroy()
    }
}
