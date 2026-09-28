plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.jianxiang.assistant"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.jianxiang.assistant"
        minSdk = 26          // Android 8.0：前台服务起始；两台真机（三星/荣耀）均覆盖
        // Phase 0：降至 28 换 W^X 豁免（Termux 运行时需 exec 应用私有目录二进制；
        // 与 Termux/dsh-mobile-apk 同构）。经模拟器 PoC 验证：exec/动态链接/rpath 全通
        targetSdk = 28
        versionCode = 2
        versionName = "0.1.6"
        ndk {
            // 引擎以 libjx.so 形式放在 jniLibs，安装时系统释放到
            // nativeLibraryDir（可执行、免解压）——targetSdk 29+ 的
            // W^X 限制下这是执行自带二进制的唯一正路
            abiFilters += listOf("arm64-v8a", "x86_64")
        }
    }

    signingConfigs {
        create("release") {
            // 开发自用签名：密钥库不存在时由 build-android.sh 生成并注明
            val ksPath = file("debug.keystore")
            if (ksPath.exists()) {
                storeFile = ksPath
                storePassword = "jianxiang"
                keyAlias = "jianxiang"
                keyPassword = "jianxiang"
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            if (file("debug.keystore").exists()) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    packaging {
        jniLibs {
            // 必须为 true：引擎是「以子进程方式执行」的，不是被
            // System.loadLibrary 加载的——库必须解压成磁盘上的真实
            // 可执行文件（nativeLibraryDir）；false 会留在 APK 内不落盘
            useLegacyPackaging = true
        }
    }
    androidResources {
        // 运行时快照已是压缩格式：避免 APK 二次压缩。注意快照不用 .gz
        // 后缀——AGP/aapt 会把 .gz 资产自动解包（文件名与体积都会变）
        noCompress += "tgz"
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.webkit:webkit:1.12.1")
}
