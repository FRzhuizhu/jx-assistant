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
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
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
            useLegacyPackaging = false
        }
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.webkit:webkit:1.12.1")
}
