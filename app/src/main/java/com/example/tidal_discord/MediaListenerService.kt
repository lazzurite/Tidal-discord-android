package com.example.tidal_discord

import android.content.ComponentName
import android.content.Context
import android.media.MediaMetadata
import android.media.session.MediaController
import android.media.session.MediaSessionManager
import android.media.session.PlaybackState
import android.os.Handler
import android.os.Looper
import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification
import android.util.Log
import bridge.Bridge

class MediaListenerService : NotificationListenerService() {

    private val controllers = mutableMapOf<String, MediaController>()
    private val callbacks = mutableMapOf<String, MediaController.Callback>()
    private lateinit var sessionManager: MediaSessionManager
    private val handler = Handler(Looper.getMainLooper())

    // Резервный опрос каждые 5 секунд, чтобы статус никогда не замораживался
    private val syncRunnable = object : Runnable {
        override fun run() {
            pollAllActiveSessions()
            handler.postDelayed(this, 5000L)
        }
    }

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

        try {
            sessionManager = getSystemService(MediaSessionManager::class.java)
            val component = ComponentName(this, MediaListenerService::class.java)
            sessionManager.addOnActiveSessionsChangedListener(sessionListener, component)
            pollAllActiveSessions()
        } catch (e: Exception) {
            Log.e("TidalDiscord", "Failed to attach MediaSessionManager", e)
        }

        handler.post(syncRunnable)
    }

    // Срабатывает мгновенно каждый раз, когда плеер меняет трек в шторке уведомлений!
    override fun onNotificationPosted(sbn: StatusBarNotification?) {
        val pkg = sbn?.packageName ?: return
        if (isSupportedPackage(pkg)) {
            pollAllActiveSessions()
        }
    }

    override fun onNotificationRemoved(sbn: StatusBarNotification?) {
        val pkg = sbn?.packageName ?: return
        if (isSupportedPackage(pkg)) {
            pollAllActiveSessions()
        }
    }

    private fun pollAllActiveSessions() {
        if (!::sessionManager.isInitialized) return
        try {
            val component = ComponentName(this, MediaListenerService::class.java)
            val activeSessions = sessionManager.getActiveSessions(component)
            var foundSupported = false

            for (controller in activeSessions) {
                if (isSupportedPackage(controller.packageName)) {
                    foundSupported = true
                    registerController(controller)
                    sendTrackToGo(controller)
                }
            }

            // Если оба плеера закрыты и сессий больше нет — очищаем статус
            if (!foundSupported && controllers.isNotEmpty()) {
                controllers.clear()
                callbacks.clear()
                Bridge.updateTrack("Music", "", "", "", false, 0L, 0L)
            }
        } catch (e: Exception) {
            Log.e("TidalDiscord", "Error polling sessions", e)
        }
    }

    private fun registerController(controller: MediaController) {
        val pkg = controller.packageName
        val existing = controllers[pkg]
        if (existing?.sessionToken == controller.sessionToken) {
            return
        }

        // Если токен сессии обновился — переподписываемся
        callbacks[pkg]?.let { oldCb -> existing?.unregisterCallback(oldCb) }

        val callback = object : MediaController.Callback() {
            override fun onMetadataChanged(metadata: MediaMetadata?) {
                sendTrackToGo(controller)
            }

            override fun onPlaybackStateChanged(state: PlaybackState?) {
                sendTrackToGo(controller)
            }

            override fun onSessionDestroyed() {
                controllers.remove(pkg)
                callbacks.remove(pkg)
                Bridge.updateTrack(
                    playerDisplayName(pkg),
                    "", "", "", false, 0L, 0L
                )
            }
        }

        controller.registerCallback(callback)
        controllers[pkg] = controller
        callbacks[pkg] = callback
        sendTrackToGo(controller)
    }

    private fun sendTrackToGo(controller: MediaController) {
        val metadata = controller.metadata
        val state = controller.playbackState
        val pbState = state?.state ?: PlaybackState.STATE_NONE

        if (pbState == PlaybackState.STATE_STOPPED || pbState == PlaybackState.STATE_NONE || pbState == PlaybackState.STATE_ERROR) {
            Bridge.updateTrack(playerDisplayName(controller.packageName), "", "", "", false, 0L, 0L)
            return
        }

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
        val playing = pbState == PlaybackState.STATE_PLAYING && title.isNotBlank()

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
        handler.removeCallbacks(syncRunnable)
        try {
            if (::sessionManager.isInitialized) {
                sessionManager.removeOnActiveSessionsChangedListener(sessionListener)
            }
            for ((pkg, controller) in controllers) {
                callbacks[pkg]?.let { controller.unregisterCallback(it) }
            }
        } catch (_: Exception) {}
        controllers.clear()
        callbacks.clear()
        Bridge.stopDiscord()
        super.onListenerDisconnected()
    }
}