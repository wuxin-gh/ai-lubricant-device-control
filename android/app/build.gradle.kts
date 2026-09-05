import java.util.Properties

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.serialization)
}

android {
    namespace = "dev.devicecontrol.app"
    compileSdk = 37

    defaultConfig {
        applicationId = "dev.devicecontrol.app"
        // Matches core's minSdk — see the note there. app cannot sit above core's
        // floor without a manifest-merger failure, and cannot sit below it either.
        minSdk = 31
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    testOptions {
        unitTests.isReturnDefaultValues = true
    }

    // 发布签名：device-control/deploy/keystore.properties（gitignored），与 mobile
    // 共用同一发布密钥（mobile/deploy/release.keystore 的副本）。不存在时回退
    // debug 签名，保证本地裸环境也能出包——与 mobile/android/app/build.gradle 的
    // 接线方式一致。装机升级要求新旧 APK 签名一致，密钥丢失即断升级链。
    signingConfigs {
        val ksPropsFile = rootProject.file("../deploy/keystore.properties")
        if (ksPropsFile.exists()) {
            create("release") {
                val ks = Properties()
                ksPropsFile.inputStream().use { ks.load(it) }
                storeFile = rootProject.file("../deploy/release.keystore")
                storePassword = ks.getProperty("storePassword")
                keyAlias = ks.getProperty("keyAlias")
                keyPassword = ks.getProperty("keyPassword")
            }
        }
    }
    buildTypes {
        release {
            signingConfig = signingConfigs.findByName("release") ?: signingConfigs.getByName("debug")
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}

dependencies {
    implementation(project(":core"))
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.appcompat)
    implementation(libs.androidx.activity.ktx)
    implementation(libs.material)
    implementation(libs.okhttp)
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.kotlinx.serialization.json)

    testImplementation(platform(libs.junit.bom))
    testImplementation(libs.junit.jupiter.api)
    testImplementation(libs.junit.jupiter.params)
    testRuntimeOnly(libs.junit.jupiter.engine)
    testRuntimeOnly(libs.junit.platform.launcher)
    testImplementation(libs.mockk)
    testImplementation(libs.kotlinx.coroutines.test)
}

tasks.withType<Test>().configureEach {
    useJUnitPlatform()
}
