package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
)

// Message represents a chat message
type Message struct {
	Username  string    `json:"username"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Channel   string    `json:"channel"`
}

// Client represents a connected WebSocket client
type Client struct {
	ID       string
	Conn     *websocket.Conn
	Username string
	Send     chan []byte
}

// Hub maintains the set of active clients and broadcasts messages to the clients
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mutex      sync.RWMutex
	redis      *redis.Client
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow connections from any origin
	},
}

func newHub() *Hub {
	// Redis client configuration - use environment variable or default
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "",
		DB:       0,
	})

	// Test Redis connection
	ctx := context.Background()
	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		log.Printf("Redis bağlantısı kurulamadı: %v", err)
		log.Println("Redis olmadan devam ediliyor...")
		rdb = nil
	} else {
		log.Println("Redis bağlantısı başarılı")
	}

	return &Hub{
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
		redis:      rdb,
	}
}

// Store message in Redis
func (h *Hub) storeMessage(msg Message) {
	if h.redis == nil {
		return
	}

	ctx := context.Background()
	messageJSON, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Mesaj serialize hatası: %v", err)
		return
	}

	// Store message in a list for the channel (keep last 100 messages)
	key := fmt.Sprintf("messages:%s", msg.Channel)
	pipe := h.redis.Pipeline()
	pipe.LPush(ctx, key, messageJSON)
	pipe.LTrim(ctx, key, 0, 99)         // Keep only the last 100 messages
	pipe.Expire(ctx, key, 24*time.Hour) // Messages expire after 24 hours

	_, err = pipe.Exec(ctx)
	if err != nil {
		log.Printf("Redis mesaj kaydetme hatası: %v", err)
	}
}

// Get recent messages from Redis for a channel
func (h *Hub) getRecentMessages(channel string, limit int) ([]Message, error) {
	if h.redis == nil {
		return []Message{}, nil
	}

	ctx := context.Background()
	key := fmt.Sprintf("messages:%s", channel)

	// Get messages (they're stored in reverse order, so we get from the end)
	results, err := h.redis.LRange(ctx, key, 0, int64(limit-1)).Result()
	if err != nil {
		return nil, err
	}

	messages := make([]Message, 0, len(results))

	// Reverse the order to show oldest first
	for i := len(results) - 1; i >= 0; i-- {
		var msg Message
		if err := json.Unmarshal([]byte(results[i]), &msg); err == nil {
			messages = append(messages, msg)
		}
	}

	return messages, nil
}

// Send recent messages to a client
func (h *Hub) sendRecentMessages(client *Client, channel string) {
	messages, err := h.getRecentMessages(channel, 50) // Send last 50 messages
	if err != nil {
		log.Printf("Geçmiş mesajları alma hatası: %v", err)
		return
	}

	log.Printf("Kanal %s için %d geçmiş mesaj gönderiliyor", channel, len(messages))

	for _, msg := range messages {
		messageJSON, err := json.Marshal(msg)
		if err != nil {
			continue
		}

		select {
		case client.Send <- messageJSON:
		default:
			// Client's send buffer is full, skip this message
			log.Printf("İstemci gönderim buffer'ı dolu, mesaj atlandı")
		}
	}
}

// Broadcast active user count to all clients
func (h *Hub) broadcastUserCount() {
	h.mutex.RLock()
	count := len(h.clients)
	h.mutex.RUnlock()

	userCountMessage := map[string]interface{}{
		"type":      "user_count",
		"count":     count,
		"timestamp": time.Now(),
	}

	messageJSON, err := json.Marshal(userCountMessage)
	if err != nil {
		log.Printf("User count message serialize hatası: %v", err)
		return
	}

	h.mutex.RLock()
	for client := range h.clients {
		select {
		case client.Send <- messageJSON:
		default:
			// Skip if client buffer is full
		}
	}
	h.mutex.RUnlock()
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			h.clients[client] = true
			h.mutex.Unlock()
			log.Printf("Yeni kullanıcı bağlandı. ID: %s", client.ID)

			// Broadcast updated user count
			go h.broadcastUserCount()

		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.Send)
				if client.Username != "" {
					log.Printf("Kullanıcı ayrıldı: %s", client.Username)
				}
				log.Printf("Bağlantı kapatıldı. ID: %s", client.ID)
			}
			h.mutex.Unlock()

			// Broadcast updated user count
			go h.broadcastUserCount()

		case message := <-h.broadcast:
			// Parse message to store in Redis
			var msg Message
			if err := json.Unmarshal(message, &msg); err == nil {
				h.storeMessage(msg)
			}

			h.mutex.RLock()
			for client := range h.clients {
				select {
				case client.Send <- message:
				default:
					close(client.Send)
					delete(h.clients, client)
				}
			}
			h.mutex.RUnlock()
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued chat messages to the current WebSocket message.
			n := len(c.Send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.Send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) readPump(hub *Hub) {
	defer func() {
		hub.unregister <- c
		c.Conn.Close()
	}()
	c.Conn.SetReadLimit(512)
	c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		_, messageBytes, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket hatası: %v", err)
			}
			break
		}

		// Parse JSON message
		var msg Message
		if err := json.Unmarshal(messageBytes, &msg); err != nil {
			log.Printf("Mesaj parse hatası: %v", err)
			// Fallback to plain text
			msg = Message{
				Username:  "Anonim",
				Message:   string(messageBytes),
				Timestamp: time.Now(),
				Channel:   "genel",
			}
		} else {
			// Update client username
			if msg.Username != "" {
				c.Username = msg.Username
			}
			// Set timestamp and default channel
			msg.Timestamp = time.Now()
			if msg.Channel == "" {
				msg.Channel = "genel"
			}

			// Handle special request for recent messages
			if msg.Message == "__GET_RECENT_MESSAGES__" {
				log.Printf("Geçmiş mesajlar istendi: kanal=%s, kullanıcı=%s", msg.Channel, msg.Username)
				go hub.sendRecentMessages(c, msg.Channel)
				continue
			}
		}

		log.Printf("Gelen mesaj: %s", string(messageBytes))

		// Broadcast the enriched message
		enrichedMessage, err := json.Marshal(msg)
		if err != nil {
			log.Printf("Mesaj JSON encode hatası: %v", err)
			continue
		}

		hub.broadcast <- enrichedMessage
	}
}

func serveWS(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade hatası: %v", err)
		return
	}

	clientID := fmt.Sprintf("%d%.3f", time.Now().Unix(), time.Now().Sub(time.Unix(time.Now().Unix(), 0)).Seconds())
	client := &Client{
		ID:   clientID,
		Conn: conn,
		Send: make(chan []byte, 256),
	}

	hub.register <- client

	// Allow collection of memory referenced by the caller by doing all work in new goroutines.
	go client.writePump()
	go client.readPump(hub)
}

func serveHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Serve the index.html file
	indexPath := filepath.Join(".", "index.html")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		log.Printf("index.html dosyası bulunamadı: %s", indexPath)
		http.Error(w, "index.html file not found", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, indexPath)
}

func (h *Hub) clearChannelHistory(channel string) error {
	if h.redis == nil {
		log.Printf("Redis bağlantısı yok, kanal geçmişi temizlenemedi: %s", channel)
		return nil
	}
	ctx := context.Background()
	key := fmt.Sprintf("messages:%s", channel)
	err := h.redis.Del(ctx, key).Err()
	if err != nil {
		log.Printf("Kanal geçmişi temizleme hatası: %v", err)
		return err
	}
	log.Printf("Kanal geçmişi temizlendi: %s", channel)
	return nil
}

func ensureSSLFiles(certFile, keyFile string) error {
	sslDir := filepath.Dir(certFile)
	// Klasör yoksa oluştur
	if _, err := os.Stat(sslDir); os.IsNotExist(err) {
		if mkerr := os.MkdirAll(sslDir, 0700); mkerr != nil {
			return fmt.Errorf("SSL klasörü oluşturulamadı: %v", mkerr)
		}
	}
	// Sertifika dosyası var mı?
	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		return fmt.Errorf("Sertifika dosyası bulunamadı: %s", certFile)
	}
	// Anahtar dosyası var mı?
	if _, err := os.Stat(keyFile); os.IsNotExist(err) {
		return fmt.Errorf("Anahtar dosyası bulunamadı: %s", keyFile)
	}
	return nil
}

func main() {
	hub := newHub()
	go hub.run()

	// Static dosyalar için handler ekle
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static/"))))

	http.HandleFunc("/", serveHome)
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWS(hub, w, r)
	})

	// Yeni endpoint: POST /clear-history
	http.HandleFunc("/clear-history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		type reqBody struct {
			Channel string `json:"channel"`
		}
		var body reqBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Channel == "" {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}
		if err := hub.clearChannelHistory(body.Channel); err != nil {
			http.Error(w, "Failed to clear history", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	const httpsPort = "443"
	const httpPort = "80"
	const host = "0.0.0.0"

	// Container içindeki SSL dosya yolları
	certFile := "/app/ssl/cloudflare-cert.pem"
	keyFile := "/app/ssl/cloudflare-key.pem"

	// SSL dosyalarını kontrol et
	if err := ensureSSLFiles(certFile, keyFile); err != nil {
		log.Printf("SSL HATASI: %v", err)
		log.Printf("HTTPS başlatılamadı. SSL sertifikalarınızı ./ssl/ klasörüne koyduğunuzdan emin olun.")

		// HTTP modunda başlat (fallback)
		log.Printf("HTTP sohbet sunucusu http://localhost:8080 adresinde başlatıldı...")
		err := http.ListenAndServe(":8080", nil)
		if err != nil {
			log.Fatal("HTTP ListenAndServe hatası: ", err)
		}
		return
	}

	// HTTPS sunucu yapılandırması
	httpsServer := &http.Server{
		Addr:    host + ":" + httpsPort,
		Handler: nil,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	log.Printf("HTTPS sohbet sunucusu https://localhost:%s adresinde başlatıldı...", httpsPort)
	log.Printf("Dış erişim için: https://melihboyaci.xyz adresini kullanın")

	// HTTP'den HTTPS'e yönlendirme sunucusu
	go func() {
		log.Printf("HTTP -> HTTPS yönlendirme sunucusu :%s portunda başlatıldı", httpPort)
		err := http.ListenAndServe(":"+httpPort, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Host header'ını koruyarak HTTPS'e yönlendir
			target := "https://" + r.Host + r.URL.RequestURI()
			log.Printf("HTTP -> HTTPS yönlendirme: %s -> %s", r.URL.String(), target)
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		}))
		if err != nil {
			log.Printf("HTTP yönlendirme sunucusu hatası: %v", err)
		}
	}()

	// HTTPS sunucusunu başlat
	err := httpsServer.ListenAndServeTLS(certFile, keyFile)
	if err != nil {
		log.Fatal("ListenAndServeTLS hatası: ", err)
	}
}
