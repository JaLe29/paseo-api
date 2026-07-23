package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"paseo-api/internal/paseo"
)

const (
	streamWriteWait  = 10 * time.Second
	streamPongWait   = 60 * time.Second
	streamPingPeriod = 25 * time.Second // must be < streamPongWait
)

var streamUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// The API-token gate already authorizes the request; these connections are
	// server-to-server, so origin is not a meaningful check.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// clientStreamMessage is a message a stream client sends to the API.
type clientStreamMessage struct {
	// "message" (send a follow-up), "stop" (interrupt), or "close" (end the stream).
	Type   string       `json:"type"`
	Text   string       `json:"text"`
	Images []imageInput `json:"images"`
}

// handleStreamAgent upgrades to a WebSocket and streams a running agent's live
// events to the client. The client may send follow-up messages and stop the
// agent over the same connection. This is purely additive — the request/response
// endpoints (POST /run, POST /agents/{id}/messages, ...) are unaffected.
func (s *Server) handleStreamAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Validate the agent exists BEFORE the upgrade: once upgraded we can only
	// speak WebSocket frames, not HTTP status codes.
	agent, err := s.client.GetAgent(r.Context(), id)
	if err != nil {
		s.respondClientError(w, "get agent failed", err)
		return
	}
	if agent == nil {
		writeError(w, http.StatusNotFound, "Agent not found.")
		return
	}

	conn, err := streamUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade writes its own HTTP error response on failure.
		s.log.Error("stream upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	// The session outlives the request context (which ends at the upgrade), so
	// give it its own background context and tear it down via defer.
	session, err := s.client.Stream(context.Background(), agent.ID)
	if err != nil {
		_ = conn.WriteJSON(paseo.StreamEvent{Type: "error", Payload: errPayload(err.Error())})
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "stream init failed"),
			time.Now().Add(streamWriteWait))
		return
	}
	defer session.Close()

	done := make(chan struct{})
	go s.streamReadLoop(conn, session, done)

	// This goroutine is the sole writer: it forwards events and sends pings.
	ticker := time.NewTicker(streamPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case ev, ok := <-session.Events():
			if !ok {
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"),
					time.Now().Add(streamWriteWait))
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(streamWriteWait))
			if err := conn.WriteJSON(ev); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(streamWriteWait))
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(streamWriteWait)); err != nil {
				return
			}
		}
	}
}

// streamReadLoop reads client control messages until the connection closes,
// signalling the writer via done. It is the sole reader of the connection.
func (s *Server) streamReadLoop(conn *websocket.Conn, session *paseo.StreamSession, done chan struct{}) {
	defer close(done)

	conn.SetReadLimit(maxBody)
	_ = conn.SetReadDeadline(time.Now().Add(streamPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(streamPongWait))
	})

	for {
		var msg clientStreamMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return // client disconnected, closed, or timed out
		}
		switch msg.Type {
		case "message":
			if msg.Text == "" {
				continue
			}
			if err := session.SendMessage(context.Background(), msg.Text, toImages(msg.Images)); err != nil {
				s.log.Error("stream send message failed", "err", err)
			}
		case "stop":
			if err := session.Stop(context.Background()); err != nil {
				s.log.Error("stream stop failed", "err", err)
			}
		case "close":
			return
		}
	}
}

// errPayload builds an {"error": "..."} JSON payload for a stream error event.
func errPayload(msg string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return b
}
