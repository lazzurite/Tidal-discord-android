package com.example.tidal_discord

import android.content.ComponentName
import android.content.Context
import android.media.MediaMetadata
import android.media.session.MediaController
import android.media.session.MediaSessionManager
import android.media.session.PlaybackState
import android.service.notification.NotificationListenerService
import android.util.Log
import bridge.Bridge

class MediaListenerService : NotificationListenerService() {

    private val controllers = mutableMapOf<MediaController, MediaController.Callback>()
    private lateinit var sessionManager: MediaSessionManager

    private fun isSupportedPackage(pkg: String): Boolean {
        return pkg == "com.aspiro.tidal" || pkg.startsWith("com.hiby.music")
    }

    private fun playerDisplayName(pkg: String): String {
        return when {
            pkg == "com.aspiro.tidal" -> "TIDAL"
            pkg.startsWith("com.hiby.music") -> "HiBy Music"
            else -> "Music"
        }
    }

    private val sessionListener =
        MediaSessionManager.OnActiveSessionsChangedListener { sessions ->
            sessions?.forEach { controller ->
                if (isSupportedPackage(controller.packageName)) {
                    registerController(controller)
                }
            }
        }

    override fun onListenerConnected() {
        super.onListenerConnected()
        Log.d("TidalDiscord", "MediaListenerService connected")

        val prefs = getSharedPreferences("discord_prefs", Context.MODE_PRIVATE)
        val token = prefs.getString("discord_token", "") ?: ""
        if (token.isNotBlank()) {
            Bridge.startDiscord(token)
        }

        sessionManager = getSystemService(MediaSessionManager::class.java)
        val component = ComponentName(this, MediaListenerService::class.java)

        sessionManager.addOnActiveSessionsChangedListener(sessionListener, component)

        sessionManager.getActiveSessions(component).forEach { controller ->
            if (isSupportedPackage(controller.packageName)) {
                registerController(controller)
            }
        }
    }

    private fun registerController(controller: MediaController) {
        if (controllers.containsKey(controller)) return

        val callback = object : MediaController.Callback() {
            override fun onMetadataChanged(metadata: MediaMetadata?) {
                sendTrackToGo(controller)
            }

            override fun onPlaybackStateChanged(state: PlaybackState?) {
                sendTrackToGo(controller)
            }

            override fun onSessionDestroyed() {
                controllers.remove(controller)
                Bridge.updateTrack(
                    playerDisplayName(controller.packageName),
                    "", "", "", false, 0L, 0L
                )
            }
        }

        controller.registerCallback(callback)
        controllers[controller] = callback
        sendTrackToGo(controller)
    }

    private fun sendTrackToGo(controller: MediaController) {
        val metadata = controller.metadata
        val state = controller.playbackState

        val title = metadata?.getString(MediaMetadata.METADATA_KEY_TITLE)
            ?.takeIf { it.isNotBlank() }
            ?: metadata?.getString(MediaMetadata.METADATA_KEY_DISPLAY_TITLE)
            ?: ""

        val artist = metadata?.getString(MediaMetadata.METADATA_KEY_ARTIST)
            ?.takeIf { it.isNotBlank() }
            ?: metadata?.getString(MediaMetadata.METADATA_KEY_ALBUM_ARTIST)
            ?: ""

        val album = metadata?.getString(MediaMetadata.METADATA_KEY_ALBUM) ?: ""
        val duration = metadata?.getLong(MediaMetadata.METADATA_KEY_DURATION) ?: 0L
        val position = state?.position ?: 0L
        val playing = state?.state == PlaybackState.STATE_PLAYING && title.isNotBlank()

        Bridge.updateTrack(
            playerDisplayName(controller.packageName),
            title,
            artist,
            album,
            playing,
            position,
            duration
        )
    }

    override fun onListenerDisconnected() {
        if (::sessionManager.isInitialized) {
            sessionManager.removeOnActiveSessionsChangedListener(sessionListener)
        }
        for ((controller, callback) in controllers) {
            controller.unregisterCallback(callback)
        }
        controllers.clear()
        Bridge.stopDiscord()
        super.onListenerDisconnected()
    }
}