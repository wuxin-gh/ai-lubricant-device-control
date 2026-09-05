package dev.devicecontrol.app.storage

import android.content.Context
import java.io.File
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

@Serializable
data class DeviceCredential(
    val serverUrl: String,
    val deviceId: String,
    val token: String,
)

class CredentialStore(
    context: Context,
    private val json: Json = Json { ignoreUnknownKeys = true },
) {
    private val file = File(context.filesDir, FILE_NAME)

    @Synchronized
    fun load(): DeviceCredential? =
        runCatching {
            if (!file.exists()) return null
            json.decodeFromString<DeviceCredential>(file.readText(Charsets.UTF_8))
        }.getOrNull()

    @Synchronized
    fun save(credential: DeviceCredential) {
        val temporary = File(file.parentFile, "$FILE_NAME.tmp")
        temporary.writeText(json.encodeToString(credential), Charsets.UTF_8)
        if (!temporary.renameTo(file)) {
            temporary.delete()
            error("Could not atomically save device credential")
        }
        file.setReadable(false, false)
        file.setReadable(true, true)
        file.setWritable(false, false)
        file.setWritable(true, true)
    }

    @Synchronized
    fun clear() {
        file.delete()
    }

    companion object {
        private const val FILE_NAME = "device-credential.json"
    }
}
