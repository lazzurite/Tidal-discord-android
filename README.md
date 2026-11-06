# TIDAL & HiBy Music — Discord Rich Presence for Android

A lightweight Discord Rich Presence integration for Android that broadcasts real-time playback status from **TIDAL** and **HiBy Music** (including bit-perfect USB Audio mode) directly to your Discord profile.

The networking core is written in **Go (Golang)** and compiled into native Android libraries via `gomobile`, handling WebSocket Gateway connections, heartbeat intervals, and asset resolution without third-party RPC intermediaries.

---

## Features

- **Multi-Player Support:** Intercepts media sessions from both **TIDAL** (`com.aspiro.tidal`) and **HiBy Music** (`com.hiby.music*`).
- **Bit-Perfect Audio Compatibility:** Uses native Android `MediaSessionManager` to read metadata even when HiBy Music operates directly through a USB DAC.
- **Dynamic Artwork Resolution:** Searches for high-resolution album covers on the fly via the iTunes Search API and proxies them through Discord's `mp:external` asset pipeline.
- **Live Progress Bar:** Calculates track start and end timestamps so Discord renders an animated playback timeline.
- **Smart Pause Handling:** Freezes the playback timeline on pause and appends a `⏸️` indicator without clearing your profile card.
- **YouTube Quick Link:** Generates an interactive "Listen on YouTube" button in the Discord profile.
- **Battery-Friendly Background Service:** Operates asynchronously using Go goroutines with reconnect backoff and minimal system footprint.

---

- **Android Client:** Kotlin, Jetpack Compose, `NotificationListenerService`, `MediaSessionManager`.
- **Core Engine:** Go (Golang), `gorilla/websocket`, `gomobile bind`.
- **Networking:** Discord Gateway v10 (`wss://gateway.discord.gg/?v=10&encoding=json`), REST API (`/applications/{id}/external-assets`).

---

## Installation & Setup

### 1. Download & Install
Download the latest `TidalDiscord-vX.X.X.apk` from the [Releases](../../releases) section and install it on your device.

### 2. Grant Android Permissions
Due to enhanced security policies on Android 13/14+ (especially on Samsung One UI):
1. Go to **Settings → Apps → Tidal & HiBy Discord**.
2. Tap the **three dots** in the top-right corner and select **Allow restricted settings** (confirm with your PIN/fingerprint).
Or just allow everything that this application require for it's work
3. Open the app and tap **Allow Notification Access**.
4. Enable the toggle for **Tidal & HiBy Discord**.
5. *(Recommended)* Set **Battery usage** to **Unrestricted** to prevent background hibernation during screen-off playback.

### 3. Connect to Discord
1. Open Discord in your desktop browser (or mobile browser in desktop mode).
2. Press `F12` to open DevTools, navigate to the **Network** tab, filter by `api`, go to Headers  and copy your `Authorization` header value.
3. Paste the token into the app's **Discord Token** input field and tap **Save & Connect**.
4. Start playing any song in TIDAL or HiBy Music — your status will appear instantly.

