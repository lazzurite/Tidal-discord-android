package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	discordAppID = "1554196425028669551"
	gatewayURL   = "wss://gateway.discord.gg/?v=10&encoding=json"
	idleTimeout  = 2 * time.Minute // Disconnect WebSocket after 2 minutes of pause
)

var (
	mu           sync.Mutex
	writeMu      sync.Mutex
	userToken    string
	wsConn       *websocket.Conn
	stopChan     chan struct{}
	isLooping    bool
	isConnected  bool
	currentTrack Track
	stateSeq     uint64
	idleTimer    *time.Timer

	// Session state for lightweight RESUME instead of heavy IDENTIFY
	sessionID  string
	resumeURL  string
	lastSeq    *int64

	artCache   = make(map[string]string)
	httpClient = &http.Client{Timeout: 8 * time.Second}
)

type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type helloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

type readyData struct {
	SessionID        string `json:"session_id"`
	ResumeGatewayURL string `json:"resume_gateway_url"`
}

// StartDiscord saves the token and connects ONLY if music is currently playing.
func StartDiscord(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}

	mu.Lock()
	tokenChanged := userToken != token
	userToken = token

	if tokenChanged {
		sessionID = ""
		resumeURL = ""
		lastSeq = nil
	}

	shouldConnect := currentTrack.Playing && currentTrack.Title != ""
	mu.Unlock()

	if tokenChanged && isLoopRunning() {
		stopLoopInternal()
	}

	if shouldConnect {
		ensureConnected()
	}
}

func StopDiscord() {
	mu.Lock()
	if idleTimer != nil {
		idleTimer.Stop()
		idleTimer = nil
	}
	mu.Unlock()
	stopLoopInternal()
}

func isLoopRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return isLooping
}

func ensureConnected() {
	mu.Lock()
	if userToken == "" || isLooping {
		mu.Unlock()
		return
	}
	stopChan = make(chan struct{})
	currentStop := stopChan
	token := userToken
	isLooping = true
	mu.Unlock()

	go connectionLoop(token, currentStop)
}

func stopLoopInternal() {
	mu.Lock()
	if stopChan != nil {
		close(stopChan)
		stopChan = nil
	}
	conn := wsConn
	wsConn = nil
	isConnected = false
	isLooping = false
	mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
}

func connectionLoop(token string, stop <-chan struct{}) {
	defer func() {
		mu.Lock()
		if stopChan == nil {
			isLooping = false
		}
		mu.Unlock()
	}()

	for {
		select {
		case <-stop:
			return
		default:
		}

		// Do not reconnect in background if music stopped playing
		mu.Lock()
		activeTrack := currentTrack.Title != "" && currentTrack.Playing
		mu.Unlock()
		if !activeTrack {
			log.Println("[GoDiscord] No active playback, stopping connection loop to save data")
			stopLoopInternal()
			return
		}

		err := runSession(token, stop)
		if err != nil {
			log.Printf("[GoDiscord] session ended: %v", err)
		}

		select {
		case <-stop:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func runSession(token string, stop <-chan struct{}) error {
	mu.Lock()
	dialURL := gatewayURL
	if resumeURL != "" && sessionID != "" {
		dialURL = resumeURL
		if !strings.Contains(dialURL, "?") {
			dialURL += "/?v=10&encoding=json"
		}
	}
	mu.Unlock()

	conn, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	if err != nil {
		mu.Lock()
		resumeURL = ""
		sessionID = ""
		mu.Unlock()
		return err
	}

	mu.Lock()
	wsConn = conn
	isConnected = false
	mu.Unlock()

	defer func() {
		mu.Lock()
		if wsConn == conn {
			wsConn = nil
			isConnected = false
		}
		mu.Unlock()
		_ = conn.Close()
	}()

	// 1. Read Opcode 10 (Hello)
	var helloMsg gatewayPayload
	if err := conn.ReadJSON(&helloMsg); err != nil {
		return err
	}
	if helloMsg.Op != 10 {
		return fmt.Errorf("expected Op 10 Hello, got Op %d", helloMsg.Op)
	}

	var hello helloData
	if err := json.Unmarshal(helloMsg.D, &hello); err != nil {
		return err
	}

	// 2. Send RESUME (Op 6) if we have a session, otherwise minimal IDENTIFY (Op 2)
	mu.Lock()
	sid := sessionID
	seqPtr := lastSeq
	mu.Unlock()

	var authPayload map[string]interface{}
	if sid != "" && seqPtr != nil {
		authPayload = map[string]interface{}{
			"op": 6,
			"d": map[string]interface{}{
				"token":      token,
				"session_id": sid,
				"seq":        *seqPtr,
			},
		}
	} else {
		authPayload = map[string]interface{}{
        			"op": 2,
        			"d": map[string]interface{}{
        				"token":        token,
        				"capabilities": 0,
        				"intents":      0,
        				"properties": map[string]string{
        					"os":      "Windows",
        					"browser": "Discord Client",
        					"device":  "",
        				},
        				"compress": false,
        			},
        		}
	}

	writeMu.Lock()
	err = conn.WriteJSON(authPayload)
	writeMu.Unlock()
	if err != nil {
		return err
	}

	// 3. Heartbeat goroutine
	hbDone := make(chan struct{})
	defer close(hbDone)

	interval := time.Duration(hello.HeartbeatInterval) * time.Millisecond
	if interval <= 0 {
		interval = 41250 * time.Millisecond
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-hbDone:
				return
			case <-ticker.C:
				mu.Lock()
				s := lastSeq
				mu.Unlock()

				writeMu.Lock()
				_ = conn.WriteJSON(map[string]interface{}{"op": 1, "d": s})
				writeMu.Unlock()
			}
		}
	}()

	// 4. Event loop
	for {
		select {
		case <-stop:
			return nil
		default:
		}

		var msg gatewayPayload
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}

		if msg.S != nil {
			mu.Lock()
			val := *msg.S
			lastSeq = &val
			mu.Unlock()
		}

		switch msg.Op {
		case 0: // Dispatch
			switch msg.T {
			case "READY":
				var r readyData
				if err := json.Unmarshal(msg.D, &r); err == nil {
					mu.Lock()
					sessionID = r.SessionID
					resumeURL = r.ResumeGatewayURL
					mu.Unlock()
				}
				onGatewayAuthenticated()
			case "RESUMED":
				onGatewayAuthenticated()
			}

		case 7: // Reconnect requested by Discord
			return fmt.Errorf("gateway requested reconnect (Op 7)")

		case 9: // Invalid Session
			mu.Lock()
			sessionID = ""
			resumeURL = ""
			lastSeq = nil
			mu.Unlock()
			return fmt.Errorf("invalid session (Op 9)")
		}
	}
}

func onGatewayAuthenticated() {
	log.Println("[GoDiscord] Gateway authenticated")
	mu.Lock()
	isConnected = true
	trackSnap := currentTrack
	seqSnap := stateSeq
	mu.Unlock()

	if trackSnap.Title != "" {
		go sendPresence(trackSnap, seqSnap)
	}
}

func UpdateTrack(
	player string,
	title string,
	artist string,
	album string,
	playing bool,
	positionMs int64,
	durationMs int64,
) {
	title = strings.TrimSpace(title)
	artist = strings.TrimSpace(artist)
	album = strings.TrimSpace(album)

	if title == "" {
		playing = false
	}

	mu.Lock()
	posDiff := positionMs - currentTrack.PositionMs
	if posDiff < 0 {
		posDiff = -posDiff
	}

	// Ignore minor position ticks only if play/pause state and track info haven't changed
	if title != "" &&
		currentTrack.Player == player &&
		currentTrack.Title == title &&
		currentTrack.Artist == artist &&
		currentTrack.Album == album &&
		currentTrack.Playing == playing &&
		posDiff < 3000 {
		mu.Unlock()
		return
	}

	stateSeq++
	currentSeq := stateSeq
	currentTrack = Track{
		Player:     player,
		Title:      title,
		Artist:     artist,
		Album:      album,
		Playing:    playing,
		PositionMs: positionMs,
		DurationMs: durationMs,
	}

	// Manage idle timer to disconnect WebSocket when music is paused/stopped
	if idleTimer != nil {
		idleTimer.Stop()
		idleTimer = nil
	}

	if !playing {
		idleTimer = time.AfterFunc(idleTimeout, func() {
			mu.Lock()
			stillPaused := !currentTrack.Playing
			seq := stateSeq
			mu.Unlock()

			if stillPaused {
				log.Println("[GoDiscord] Idle timeout reached, clearing presence and closing WebSocket")
				sendPresence(Track{}, seq)
				time.Sleep(300 * time.Millisecond)
				stopLoopInternal()
			}
		})
	}

	ready := isConnected
	mu.Unlock()

	if playing {
		ensureConnected()
	}

	if ready {
		go sendPresence(currentTrack, currentSeq)
	}
}

func sendPresence(t Track, seq uint64) {
	var activities []map[string]interface{}

	if t.Title != "" {
		// Resolve cover BEFORE checking seq, because HTTP lookup takes time
		var mpAsset string
		if t.Playing {
			mpAsset = resolveDiscordCover(t.Artist, t.Title, t.Album)
		} else {
			// On pause, only use already-cached cover so pause updates instantly
			cacheKey := t.Artist + "|" + t.Album + "|" + t.Title
			mu.Lock()
			if cached, ok := artCache[cacheKey]; ok && cached != "none" {
				mpAsset = cached
			}
			mu.Unlock()
		}

		nowMs := time.Now().UnixMilli()
		ytQuery := url.QueryEscape(t.Artist + " - " + t.Title)
		ytURL := "https://www.youtube.com/results?search_query=" + ytQuery

		stateText := t.Artist
		if !t.Playing {
			stateText = t.Artist + " • Paused ⏸️"
		}

		activity := map[string]interface{}{
			"name":           t.Player,
			"type":           2,
			"application_id": discordAppID,
			"details":        t.Title,
			"state":          stateText,
			"buttons": []string{
				"Listen on YouTube",
			},
			"metadata": map[string]interface{}{
				"button_urls": []string{
					ytURL,
				},
			},
		}

		if t.Playing {
			startMs := nowMs - t.PositionMs
			if t.DurationMs > 0 {
				activity["timestamps"] = map[string]int64{
					"start": startMs,
					"end":   startMs + t.DurationMs,
				}
			} else {
				activity["timestamps"] = map[string]int64{
					"start": startMs,
				}
			}
		}

		assets := map[string]string{}
		if t.Album != "" {
			assets["large_text"] = t.Album
		} else {
			assets["large_text"] = "Listening to " + t.Player
		}

		if mpAsset != "" {
			assets["large_image"] = mpAsset
		}
		activity["assets"] = assets

		activities = append(activities, activity)
	}

	// Abort if a newer play/pause/stop event arrived while we were resolving artwork
	mu.Lock()
	if seq != stateSeq {
		mu.Unlock()
		return
	}
	conn := wsConn
	ready := isConnected
	mu.Unlock()

	if conn == nil || !ready {
		return
	}

	payload := map[string]interface{}{
		"op": 3,
		"d": map[string]interface{}{
			"since":      0,
			"activities": activities,
			"status":     "online",
			"afk":        false,
		},
	}

	writeMu.Lock()
	err := conn.WriteJSON(payload)
	writeMu.Unlock()

	if err != nil {
		log.Printf("[GoDiscord] failed to send presence: %v", err)
	} else {
		log.Printf("[GoDiscord] updated presence: [%s] %s - %s (playing=%v)", t.Player, t.Artist, t.Title, t.Playing)
	}
}

func resolveDiscordCover(artist, title, album string) string {
	cacheKey := artist + "|" + album + "|" + title
	mu.Lock()
	if cached, ok := artCache[cacheKey]; ok {
		mu.Unlock()
		if cached == "none" {
			return ""
		}
		return cached
	}
	token := userToken
	mu.Unlock()

	rawURL := fetchItunesArtwork(artist, title, album)
	if rawURL == "" || token == "" {
		mu.Lock()
		artCache[cacheKey] = "none" // Cache negative result to avoid repeated lookups
		mu.Unlock()
		return ""
	}

	bodyBytes, _ := json.Marshal(map[string][]string{
		"urls": {rawURL},
	})
	req, err := http.NewRequest(
		"POST",
		"https://discord.com/api/v10/applications/"+discordAppID+"/external-assets",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var parsed []struct {
		ExternalAssetPath string `json:"external_asset_path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil || len(parsed) == 0 {
		return ""
	}

	mpURL := "mp:" + parsed[0].ExternalAssetPath
	mu.Lock()
	artCache[cacheKey] = mpURL
	mu.Unlock()
	return mpURL
}

func fetchItunesArtwork(artist, title, album string) string {
	q := strings.TrimSpace(artist + " " + album)
	if album == "" {
		q = strings.TrimSpace(artist + " " + title)
	}
	endpoint := "https://itunes.apple.com/search?media=music&limit=1&term=" + url.QueryEscape(q)
	resp, err := httpClient.Get(endpoint)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var data struct {
		Results []struct {
			ArtworkUrl100 string `json:"artworkUrl100"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || len(data.Results) == 0 {
		return ""
	}
	return strings.Replace(data.Results[0].ArtworkUrl100, "100x100bb", "512x512bb", 1)
}