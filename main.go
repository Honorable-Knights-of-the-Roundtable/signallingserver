package main

import (
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"sync"

	"github.com/Honorable-Knights-of-the-Roundtable/signallingserver/config"
	"github.com/gorilla/websocket"
	"github.com/spf13/viper"
)

// WSMessage is the envelope for all signalling messages.
// The server only inspects Type, To, and From — Data is forwarded opaquely.
type WSMessage struct {
	Type string          `json:"type"` // "register", "offer", "answer"
	To   string          `json:"to,omitempty"`
	From string          `json:"from,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

var upgrader = websocket.Upgrader{
	// Allow all origins for now — tighten this for production if needed
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Server struct {
	peers    map[string]*threadSafeWriter
	rooms    map[string][]string // room name → peer UUIDs
	peerRoom map[string]string   // peer UUID → room name (for cleanup on disconnect)
	mu       sync.RWMutex
}

func NewServer() *Server {
	return &Server{
		peers:    make(map[string]*threadSafeWriter),
		rooms:    make(map[string][]string),
		peerRoom: make(map[string]string),
	}
}

func (s *Server) register(id string, conn *threadSafeWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[id] = conn
	slog.Info("peer registered", "id", id, "total_peers", len(s.peers))
}

func (s *Server) deregister(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.peers, id)
	s.disconnectRoomLocked(id)

	slog.Info("peer deregistered", "id", id, "total_peers", len(s.peers))
}

// joinRoom adds peerID to the room and returns the UUIDs of peers already there.
// Must be called with mu held for writing or use the public wrapper below.
func (s *Server) joinRoom(peerID, roomName string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing := make([]string, len(s.rooms[roomName]))
	copy(existing, s.rooms[roomName])

	s.rooms[roomName] = append(s.rooms[roomName], peerID)
	s.peerRoom[peerID] = roomName

	slog.Info("peer joined room", "peer", peerID, "room", roomName, "existing_peers", len(existing))
	return existing
}

// disconnectRoomLocked removes peerID from their room. Caller must hold mu for writing.
func (s *Server) disconnectRoomLocked(peerID string) {
	roomName, ok := s.peerRoom[peerID]
	if !ok {
		return
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
	}
	slog.Info("peer left room", "peer", peerID, "room", roomName)
}

// disconnectRoom removes peerID from their room, acquiring the lock itself.
func (s *Server) disconnectRoom(peerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disconnectRoomLocked(peerID)
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
	s.register(peerID, safe)
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
			existing := s.joinRoom(peerID, data.Room)
			peersPayload, _ := json.Marshal(struct {
				Peers []string `json:"peers"`
			}{Peers: existing})
			safe.WriteJSON(WSMessage{
				Type: "room-peers",
				Data: peersPayload,
			})

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
