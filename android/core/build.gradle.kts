plugins {
    alias(libs.plugins.android.library)
    // No kotlin.android plugin: AGP 9 ships built-in Kotlin support
    // (https://kotl.in/gradle/agp-built-in-kotlin). The vendored ARC-MCP data classes
    // (AccessibilityNodeData, ScreenInfo, ScreenshotData, ...) are @Serializable, so
    // the serialization plugin is still needed.
    alias(libs.plugins.kotlin.serialization)
}

android {
    namespace = "dev.devicecontrol.core"
    compileSdk = 37

    defaultConfig {
        // minSdk 31 covers Android 12 devices (the test hardware is a Huawei P40 Pro,
        // API 31). ARC-MCP shipped minSdk 33 for its accessibility IME, but that one
        // API — android.accessibilityservice.InputMethod (API 33) backing type_text —
        // is the ONLY hard 33 dependency. It's gated at registration by
        // Capabilities.forApiLevel(): API < 33 devices simply don't declare type_text,
        // so the server never routes it to them. Screenshot (takeScreenshot, API 30),
        // gestures, tree reads, and global actions all work on 31.
        minSdk = 31
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    // Unit tests read android.* classes that the stub android.jar leaves
    // unimplemented; returning defaults instead of throwing lets pure-logic
    // tests (tree formatting, redaction) run without an emulator.
    testOptions {
        unitTests.isReturnDefaultValues = true
    }
}

kotlin {
    compilerOptions {
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}

dependencies {
    implementation(libs.kotlinx.coroutines.core)
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.kotlinx.serialization.json)

    // javax.inject only — the @Inject annotations on the reused ARC-MCP classes
    // are kept so the sources stay close to upstream, but this library brings no
    // DI runtime: the consuming app decides how to construct these objects.
    implementation(libs.javax.inject)

    // JUnit 5 + mockk: the vendored ARC-MCP tests are written against these, so
    // they can come across unmodified rather than being rewritten for JUnit 4.
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
