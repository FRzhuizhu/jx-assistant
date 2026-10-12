package com.jianxiang.link

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.VibrationEffect
import android.os.Vibrator
import android.widget.Button
import android.widget.TextView
import androidx.activity.ComponentActivity
import androidx.camera.core.Camera
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.MultiFormatReader
import com.google.zxing.NotFoundException
import com.google.zxing.RGBLuminanceSource
import com.google.zxing.common.HybridBinarizer
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

/**
 * 扫码连接：CameraX 预览 + ImageAnalysis 逐帧 ZXing 解码（QR_CODE）。
 * 电脑屏幕上的码多为横向，手机竖持时旋转 90°——四个方向各试一次。
 * 解到结果：震动一下，setResult 带 entry 文本回首页。
 */
class ScanActivity : ComponentActivity() {

    private lateinit var previewView: PreviewView
    private lateinit var permHint: TextView
    private var camera: Camera? = null
    private var torchOn = false
    private var analyzerExecutor: ExecutorService? = null
    private val main = Handler(Looper.getMainLooper())
    private var done = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_scan)
        previewView = findViewById(R.id.preview)
        permHint = findViewById(R.id.perm_hint)
        findViewById<Button>(R.id.btn_torch).setOnClickListener {
            torchOn = !torchOn
            camera?.cameraControl?.enableTorch(torchOn)
        }
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA)
            == PackageManager.PERMISSION_GRANTED
        ) {
            startCamera()
        } else {
            requestPermissions(arrayOf(Manifest.permission.CAMERA), REQ_CAM)
        }
    }

    override fun onRequestPermissionsResult(
        requestCode: Int, permissions: Array<String>, grantResults: IntArray,
    ) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQ_CAM) {
            if (grantResults.isNotEmpty() && grantResults[0] == PackageManager.PERMISSION_GRANTED) startCamera()
            else permHint.visibility = android.view.View.VISIBLE
        }
    }

    private fun startCamera() {
        val executor = Executors.newSingleThreadExecutor()
        analyzerExecutor = executor
        val future = ProcessCameraProvider.getInstance(this)
        future.addListener({
            try {
                val provider = future.get()
                val preview = Preview.Builder().build().also {
                    it.setSurfaceProvider(previewView.surfaceProvider)
                }
                val analysis = ImageAnalysis.Builder()
                    .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                    .build()
                analysis.setAnalyzer(executor, ::analyzeFrame)
                provider.unbindAll()
                camera = provider.bindToLifecycle(
                    this, CameraSelector.DEFAULT_BACK_CAMERA, preview, analysis,
                )
            } catch (e: Exception) {
                permHint.text = "相机启动失败：${e.message}"
                permHint.visibility = android.view.View.VISIBLE
            }
        }, ContextCompat.getMainExecutor(this))
    }

    /* ---------- 解码（四向旋转各试一次） ---------- */

    private fun analyzeFrame(img: ImageProxy) {
        if (done) {
            img.close(); return
        }
        try {
            val bmp = img.toBitmap() // YUV→RGB（camera-core 1.3+）
            if (bmp != null) {
                val w = bmp.width
                val h = bmp.height
                val pixels = IntArray(w * h)
                bmp.getPixels(pixels, 0, w, 0, 0, w, h)
                bmp.recycle()
                val text = tryDecode(pixels, w, h)
                if (text != null) onFound(text)
            }
        } catch (e: Exception) {
            // 单帧失败静默（NotFoundException 等）
        } finally {
            img.close()
        }
    }

    private fun tryDecode(pixels: IntArray, w: Int, h: Int): String? {
        for (rot in 0 until 4) {
            val (pw, ph) = if (rot % 2 == 0) w to h else h to w
            val rotated = rotatePixels(pixels, w, h, rot)
            val reader = MultiFormatReader().apply {
                setHints(mapOf(DecodeHintType.POSSIBLE_FORMATS to listOf(BarcodeFormat.QR_CODE)))
            }
            try {
                val src = RGBLuminanceSource(pw, ph, rotated)
                val result = reader.decodeWithState(BinaryBitmap(HybridBinarizer(src)))
                return result.text
            } catch (e: NotFoundException) {
                // 试下一个方向
            } catch (e: Exception) {
                // 坏帧，放弃本帧
                return null
            } finally {
                reader.reset()
            }
        }
        return null
    }

    /** 旋转 pixels（0/90/180/270 度，顺时针）到新数组 */
    private fun rotatePixels(src: IntArray, w: Int, h: Int, rot: Int): IntArray {
        if (rot == 0) return src
        val out = IntArray(src.size)
        when (rot) {
            1 -> for (y in 0 until h) for (x in 0 until w) out[x * h + (h - 1 - y)] = src[y * w + x]
            2 -> for (y in 0 until h) for (x in 0 until w) out[(h - 1 - y) * w + (w - 1 - x)] = src[y * w + x]
            3 -> for (y in 0 until h) for (x in 0 until w) out[(w - 1 - x) * h + y] = src[y * w + x]
        }
        return out
    }

    private fun onFound(text: String) {
        done = true
        main.post {
            try {
                val v = getSystemService(Vibrator::class.java)
                v?.vibrate(VibrationEffect.createOneShot(80, VibrationEffect.DEFAULT_AMPLITUDE))
            } catch (e: Exception) {
            }
            setResult(RESULT_OK, Intent().putExtra("entry", text))
            finish()
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        analyzerExecutor?.shutdown()
    }

    companion object {
        private const val REQ_CAM = 2
    }
}
