/*
 * Vendored from android-remote-control-mcp (MIT), Copyright (c) Daniele Salvatore Albano.
 * See NOTICE at the repository root. Changes: package renamed for device-control.
 */
package dev.devicecontrol.core.accessibility

import javax.inject.Inject
import javax.inject.Singleton

@Singleton
class ScreenStateSnapshotCacheImpl
    @Inject
    constructor() : ScreenStateSnapshotCache {
        @Volatile
        private var snapshot: ScreenStateSnapshot? = null

        override fun store(snapshot: ScreenStateSnapshot) {
            this.snapshot = snapshot
        }

        override fun get(id: String): ScreenStateSnapshot? {
            val current = snapshot
            return if (current != null && current.id == id) current else null
        }

        override fun clear() {
            snapshot = null
        }
    }
