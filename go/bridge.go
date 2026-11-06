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
)

var (
	mu           sync.Mutex
	writeMu      sync.Mutex // Websocket protection
	userToken    string
	wsConn       *websocket.Conn
	stopChan     chan struct{}
	isConnected  bool
	currentTrack Track
	artCache     = make(map[string]string)
	httpClient   = &http.Client{Timeout: 8 * time.Second}
)

type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	T  string          `json:"t,omitempty"`
}

type helloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

func StartDiscord(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}

	mu.Lock()
	if isConnected && userToken == token {
		mu.Unlock()
		return
	}
	userToken = token
	if stopChan != nil {
		close(stopChan)
	}
	stopChan = make(chan struct{})
	currentStop := stopChan
	mu.Unlock()

	go connectionLoop(token, currentStop)
}

func StopDiscord() {
	mu.Lock()
	defer mu.Unlock()
	if stopChan != nil {
		close(stopChan)
		stopChan = nil
	}
	if wsConn != nil {
		_ = wsConn.Close()
		wsConn = nil
	}
	isConnected = false
}

func connectionLoop(token string, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}

		err := runSession(token, stop)
		if err != nil {
			log.Printf("[GoDiscord] session ended: %v, reconnecting in 5s...", err)
		}

		select {
		case <-stop:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func runSession(token string, stop <-chan struct{}) error {
	conn, _, err := websocket.DefaultDialer.Dial(gatewayURL, nil)
	if err != nil {
		return err
	}

	mu.Lock()
	wsConn = conn
	isConnected = false
	mu.Unlock()

	defer func() {
		mu.Lock()
		isConnected = false
		_ = conn.Close()
		mu.Unlock()
	}()

	// 1. Waiting for Opcode 10 (Hello)
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

	// 2 Sending Opcode 10 (Identify)
	identify := map[string]interface{}{
		"op": 2,
		"d": map[string]interface{}{
			"token":        token,
			"capabilities": 16381,
			"properties": map[string]string{
				"os":      "Android",
				"browser": "Discord Android",
				"device":  "Android",
			},
		},
	}

	writeMu.Lock()
	err = conn.WriteJSON(identify)
	writeMu.Unlock()
	if err != nil {
		return err
	}

	// 3. Launching Heartbeat
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
				writeMu.Lock()
				if conn != nil {
					_ = conn.WriteJSON(map[string]interface{}{"op": 1, "d": nil})
				}
				writeMu.Unlock()
			}
		}
	}()

	// 4. Listening events
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

		// Discord approved auth
		if msg.Op == 0 && (msg.T == "READY" || msg.T == "RESUMED") {
			log.Println("[GoDiscord] Gateway authenticated (READY received)")
			mu.Lock()
			isConnected = true
			trackSnap := currentTrack
			mu.Unlock()

			if trackSnap.Playing && trackSnap.Title != "" {
				go sendPresence(trackSnap)
			}
		}
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

	mu.Lock()
	posDiff := positionMs - currentTrack.PositionMs
	if posDiff < 0 {
		posDiff = -posDiff
	}

	if currentTrack.Player == player &&
		currentTrack.Title == title &&
		currentTrack.Artist == artist &&
		currentTrack.Album == album &&
		currentTrack.Playing == playing &&
		posDiff < 3000 {
		mu.Unlock()
		return
	}

	currentTrack = Track{
		Player:     player,
		Title:      title,
		Artist:     artist,
		Album:      album,
		Playing:    playing,
		PositionMs: positionMs,
		DurationMs: durationMs,
	}
	ready := isConnected
	mu.Unlock()

	if ready {
		go sendPresence(currentTrack)
	}
}

func sendPresence(t Track) {
	var activities []map[string]interface{}

	if t.Title != "" {
		nowMs := time.Now().UnixMilli()

		// Генерируем поисковую ссылку на YouTube
		ytQuery := url.QueryEscape(t.Artist + " - " + t.Title)
		ytURL := "https://www.youtube.com/results?search_query=" + ytQuery

		stateText := t.Artist
		if !t.Playing {
			stateText = t.Artist + " Paused ⏸️"
		}

		activity := map[string]interface{}{
			"name":           t.Player,
			"type":           2, // 2 = Listening to ...
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

		// Only when music plays
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

		// Album picture
		assets := map[string]string{}
		if t.Album != "" {
			assets["large_text"] = t.Album
		} else {
			assets["large_text"] = t.Player
		}

		if mpAsset := resolveDiscordCover(t.Artist, t.Title, t.Album); mpAsset != "" {
			assets["large_image"] = mpAsset
		}
		activity["assets"] = assets

		activities = append(activities, activity)
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

	mu.Lock()
	conn := wsConn
	ready := isConnected
	mu.Unlock()

	if conn != nil && ready {
		writeMu.Lock()
		err := conn.WriteJSON(payload)
		writeMu.Unlock()

		if err != nil {
			log.Printf("[GoDiscord] failed to send presence: %v", err)
		} else {
			log.Printf("[GoDiscord] updated presence: [%s] %s - %s (playing=%v)", t.Player, t.Artist, t.Title, t.Playing)
		}
	}
}

func resolveDiscordCover(artist, title, album string) string {
	cacheKey := artist + "|" + album + "|" + title
	mu.Lock()
	if cached, ok := artCache[cacheKey]; ok {
		mu.Unlock()
		return cached
	}
	token := userToken
	mu.Unlock()

	rawURL := fetchItunesArtwork(artist, title, album)
	if rawURL == "" || token == "" {
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