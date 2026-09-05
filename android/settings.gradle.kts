pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "device-control-android"

// core/ — reusable accessibility + screen-capture library.
// app/ — the device dialer, protocol client, foreground service, and minimal UI.
include(":core", ":app")
