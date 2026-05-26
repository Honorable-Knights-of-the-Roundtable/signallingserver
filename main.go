package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"unicode"

	"github.com/Honorable-Knights-of-the-Roundtable/signallingserver/config"
	"github.com/gorilla/websocket"
	"github.com/spf13/viper"
)

const maxUsernameLen = 64

// sanitizeName strips control characters, trims whitespace, and caps length.
// Returns an empty string if nothing printable remains.
func sanitizeName(name string) string {
	var runes []rune
	for _, r := range name {
		if !unicode.IsControl(r) {
			runes = append(runes, r)
		}
	}
	name = strings.TrimSpace(string(runes))
	if len([]rune(name)) > maxUsernameLen {
		name = string([]rune(name)[:maxUsernameLen])
	}
	return name
}

// WSMessage is the envelope for all signalling messages.
// The server only inspects Type, To, and From — Data is forwarded opaquely.
type WSMessage struct {
	Type string          `json:"type"` // "register", "offer", "answer"
	To   string          `json:"to,omitempty"`
	From string          `json:"from,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

type peerInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var upgrader = websocket.Upgrader{
	// Allow all origins for now — tighten this for production if needed
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Server struct {
	peers    map[string]*threadSafeWriter
	names    map[string]string   // peer UUID → display name
	rooms    map[string][]string // room name → peer UUIDs
	peerRoom map[string]string   // peer UUID → room name (for cleanup on disconnect)
	mu       sync.RWMutex
}

func NewServer() *Server {
	return &Server{
		peers:    make(map[string]*threadSafeWriter),
		names:    make(map[string]string),
		rooms:    make(map[string][]string),
		peerRoom: make(map[string]string),
	}
}

func (s *Server) register(id, name string, conn *threadSafeWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[id] = conn
	s.names[id] = name
	slog.Info("peer registered", "id", id, "name", name, "total_peers", len(s.peers))
}

func (s *Server) deregister(id string) {
	s.mu.Lock()
	remaining := s.disconnectRoomLocked(id)
	delete(s.peers, id)
	delete(s.names, id)
	s.mu.Unlock()

	if remaining != nil {
		s.broadcastRoomUpdate(remaining)
	}
	slog.Info("peer deregistered", "id", id, "total_peers", len(s.peers))
}

// joinRoom adds peerID to the room and returns the full member list (including the new peer).
// Returns (nil, true) if the peer is already in that room.
func (s *Server) joinRoom(peerID, roomName string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if current, ok := s.peerRoom[peerID]; ok && current == roomName {
		slog.Info("peer already in room, rejecting duplicate join", "peer", peerID, "room", roomName)
		return nil, true
	}

	s.rooms[roomName] = append(s.rooms[roomName], peerID)
	s.peerRoom[peerID] = roomName

	all := make([]string, len(s.rooms[roomName]))
	copy(all, s.rooms[roomName])
	slog.Info("peer joined room", "peer", peerID, "room", roomName, "total_peers", len(all))
	return all, false
}

// disconnectRoomLocked removes peerID from their room and returns the remaining members.
// Returns nil if the peer was not in a room or the room is now empty.
// Caller must hold mu for writing.
func (s *Server) disconnectRoomLocked(peerID string) []string {
	roomName, ok := s.peerRoom[peerID]
	if !ok {
		return nil
	}
	delete(s.peerRoom, peerID)
	peers := s.rooms[roomName]
	for i, id := range peers {
		if id == peerID {
			s.rooms[roomName] = append(peers[:i], peers[i+1:]...)
			break
		}
	}
	if len(s.rooms[roomName]) == 0 {
		delete(s.rooms, roomName)
		slog.Info("peer left room (room now empty)", "peer", peerID, "room", roomName)
		return nil
	}
	remaining := make([]string, len(s.rooms[roomName]))
	copy(remaining, s.rooms[roomName])
	slog.Info("peer left room", "peer", peerID, "room", roomName, "remaining", len(remaining))
	return remaining
}

// disconnectRoom removes peerID from their room and broadcasts the updated member list.
func (s *Server) disconnectRoom(peerID string) {
	s.mu.Lock()
	remaining := s.disconnectRoomLocked(peerID)
	s.mu.Unlock()

	if remaining != nil {
		s.broadcastRoomUpdate(remaining)
	}
}

// broadcastRoomUpdate sends the full member list (with display names) to every peer in that list.
func (s *Server) broadcastRoomUpdate(memberIDs []string) {
	s.mu.RLock()
	peers := make([]peerInfo, 0, len(memberIDs))
	conns := make([]*threadSafeWriter, 0, len(memberIDs))
	for _, id := range memberIDs {
		name := s.names[id]
		if name == "" {
			name = id
		}
		peers = append(peers, peerInfo{ID: id, Name: name})
		if conn, ok := s.peers[id]; ok {
			conns = append(conns, conn)
		}
	}
	s.mu.RUnlock()

	payload, _ := json.Marshal(struct {
		Peers []peerInfo `json:"peers"`
	}{Peers: peers})
	msg := WSMessage{Type: "room-update", Data: payload}

	for _, conn := range conns {
		conn.WriteJSON(msg)
	}
}

func (s *Server) route(msg WSMessage) {
	s.mu.RLock()
	target, ok := s.peers[msg.To]
	s.mu.RUnlock()

	if !ok {
		slog.Warn("target peer not connected", "to", msg.To, "from", msg.From)
		return
	}
	if err := target.WriteJSON(msg); err != nil {
		slog.Error("failed to forward message", "to", msg.To, "from", msg.From, "err", err)
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	safe := &threadSafeWriter{Conn: conn}

	// First message must be a register
	var msg WSMessage
	if err := conn.ReadJSON(&msg); err != nil || msg.Type != "register" || msg.From == "" {
		slog.Warn("first message was not a valid register", "err", err)
		return
	}

	peerID := msg.From
	var regData struct {
		Name string `json:"name"`
	}
	if msg.Data != nil {
		json.Unmarshal(msg.Data, &regData)
	}
	name := sanitizeName(regData.Name)
	if name == "" {
		name = peerID
	}

	s.register(peerID, name, safe)
	defer s.deregister(peerID)

	// Handle all subsequent messages
	for {
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Error("websocket read error", "peer", peerID, "err", err)
			}
			return
		}
		msg.From = peerID // stamp the sender so the recipient knows who sent it

		switch msg.Type {
		case "join":
			var data struct {
				Room string `json:"room"`
			}
			if err := json.Unmarshal(msg.Data, &data); err != nil || data.Room == "" {
				slog.Warn("invalid join message", "peer", peerID, "err", err)
				continue
			}
			all, duplicate := s.joinRoom(peerID, data.Room)
			if duplicate {
				errPayload, _ := json.Marshal(struct {
					Message string `json:"message"`
				}{Message: "already in room " + data.Room})
				safe.WriteJSON(WSMessage{Type: "error", Data: errPayload})
				continue
			}
			s.broadcastRoomUpdate(all)

		case "rename":
			var data struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(msg.Data, &data); err != nil {
				slog.Warn("invalid rename message", "peer", peerID, "err", err)
				continue
			}
			data.Name = sanitizeName(data.Name)
			if data.Name == "" {
				slog.Warn("rename rejected: empty name after sanitization", "peer", peerID)
				continue
			}
			s.mu.Lock()
			s.names[peerID] = data.Name
			roomName := s.peerRoom[peerID]
			var members []string
			if roomName != "" {
				members = make([]string, len(s.rooms[roomName]))
				copy(members, s.rooms[roomName])
			}
			s.mu.Unlock()
			slog.Info("peer renamed", "peer", peerID, "name", data.Name)
			if roomName != "" {
				s.broadcastRoomUpdate(members)
			}

		case "disconnect":
			slog.Debug("disconnecting", "type", msg.Type, "from", peerID, "to", msg.To)
			s.disconnectRoom(peerID)

		default:
			slog.Debug("routing message", "type", msg.Type, "from", peerID, "to", msg.To)
			s.route(msg)
		}
	}
}

// threadSafeWriter wraps a websocket.Conn with a mutex since gorilla websocket
// does not support concurrent writes.
type threadSafeWriter struct {
	*websocket.Conn
	sync.Mutex
}

func (t *threadSafeWriter) WriteJSON(v any) error {
	t.Lock()
	defer t.Unlock()
	return t.Conn.WriteJSON(v)
}

func main() {
	configFilePath := flag.String("configFilePath", "config.yaml", "Path to config file.")
	flag.Parse()

	config.LoadConfig(*configFilePath)
	logFilePointer, err := config.ConfigureDefaultLogger(
		viper.GetString("loglevel"),
		viper.GetString("logfile"),
		slog.HandlerOptions{},
	)
	if err != nil {
		slog.Error("error while configuring default logger", "err", err)
		panic(err)
	}
	if logFilePointer != nil {
		defer logFilePointer.Close()
	}

	server := NewServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", server.handleWS)

	listenAddress := viper.GetString("localaddress")
	slog.Info("starting signalling server", "address", listenAddress)
	if err := http.ListenAndServe(listenAddress, mux); err != nil {
		slog.Error("server failed", "err", err)
	}
}
