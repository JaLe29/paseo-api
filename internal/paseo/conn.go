package paseo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// daemonConn is a single short-lived WebSocket connection to the paseo daemon.
// It speaks the same wire protocol as the official paseo CLI: a top-level
// {"type":"session","message":{...}} envelope in both directions, with requests
// correlated to responses by a "requestId" field.
type daemonConn struct {
	ws  *websocket.Conn
	log *slog.Logger

	writeMu sync.Mutex // serializes writes (gorilla allows only one concurrent writer)

	mu      sync.Mutex
	waiters map[string]chan inbound // requestId -> response channel
	subs    map[int]chan inbound    // subscriber id -> broadcast channel
	subSeq  int                     // monotonic subscriber id counter
	closed  bool

	serverInfo chan struct{} // closed once the server_info handshake message arrives
	fatal      chan error    // receives the first fatal read-loop error
}

// inbound is a decoded session-level message from the daemon.
type inbound struct {
	Type    string          // session message type, e.g. "workspace.create.response"
	Payload json.RawMessage // the "payload" object (or the whole message for status)
}

// dial opens the WebSocket, performs the hello handshake and blocks until the
// daemon sends its server_info status (the "ready" signal).
func (c *Client) dial(ctx context.Context) (*daemonConn, error) {
	wsURL := url.URL{Scheme: "ws", Host: c.opts.Host, Path: "/ws"}

	header := http.Header{}
	var subprotocols []string
	if pw := strings.TrimSpace(c.opts.Password); pw != "" {
		header.Set("Authorization", "Bearer "+pw)
		subprotocols = []string{"paseo.bearer." + pw}
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: c.opts.ConnectTimeout,
		Subprotocols:     subprotocols,
	}

	ws, _, err := dialer.DialContext(ctx, wsURL.String(), header)
	if err != nil {
		return nil, fmt.Errorf("connect to paseo daemon at %s: %w", c.opts.Host, err)
	}

	conn := &daemonConn{
		ws:         ws,
		log:        c.log,
		waiters:    make(map[string]chan inbound),
		subs:       make(map[int]chan inbound),
		serverInfo: make(chan struct{}),
		fatal:      make(chan error, 1),
	}

	go conn.readLoop()
	go conn.keepAlive()

	// Send the hello handshake (top-level message, not session-wrapped).
	hello := map[string]any{
		"type":            "hello",
		"clientId":        uuid.NewString(),
		"clientType":      "cli",
		"protocolVersion": 1,
		"capabilities": map[string]bool{
			"custom_mode_icons":            true,
			"reasoning_merge_enum":         true,
			"terminal_reflowable_snapshot": true,
		},
		"appVersion": c.opts.AppVersion,
	}
	if err := conn.writeJSON(hello); err != nil {
		conn.close()
		return nil, fmt.Errorf("send hello: %w", err)
	}

	// Wait for server_info before sending any RPC.
	select {
	case <-conn.serverInfo:
		return conn, nil
	case err := <-conn.fatal:
		conn.close()
		return nil, fmt.Errorf("handshake failed: %w", err)
	case <-ctx.Done():
		conn.close()
		return nil, ctx.Err()
	case <-time.After(c.opts.ConnectTimeout):
		conn.close()
		return nil, fmt.Errorf("timed out waiting for daemon handshake")
	}
}

func (c *daemonConn) writeJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, data)
}

// keepAlive sends the protocol's WS-level ping ({"type":"ping"}) every 10s so the
// connection survives multi-minute agent runs. The daemon replies with a bare
// {"type":"pong"} which the read loop ignores.
func (c *daemonConn) keepAlive() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return
		}
		if err := c.writeJSON(map[string]string{"type": "ping"}); err != nil {
			return
		}
	}
}

// readLoop reads messages, unwraps the session envelope and routes responses to
// the waiter registered for their requestId.
func (c *daemonConn) readLoop() {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			c.failAll(fmt.Errorf("connection closed: %w", err))
			return
		}

		var outer struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(data, &outer); err != nil {
			continue
		}
		// Bare WS-level keepalive pong — ignore.
		if outer.Type == "pong" || outer.Type != "session" {
			continue
		}

		var msg struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(outer.Message, &msg); err != nil {
			continue
		}

		// Peek requestId and status from the payload (status messages carry both).
		var meta struct {
			RequestID string `json:"requestId"`
			Status    string `json:"status"`
		}
		_ = json.Unmarshal(msg.Payload, &meta)

		// The server_info status is the handshake-complete signal (no requestId).
		if msg.Type == "status" && meta.Status == "server_info" {
			c.signalServerInfo()
			continue
		}

		if meta.RequestID == "" {
			// Broadcast/streaming message with no correlation — fan it out to
			// any stream subscribers instead of dropping it.
			c.publish(inbound{Type: msg.Type, Payload: msg.Payload})
			continue
		}

		c.deliver(meta.RequestID, inbound{Type: msg.Type, Payload: msg.Payload})
	}
}

func (c *daemonConn) signalServerInfo() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.serverInfo:
		// already signaled
	default:
		close(c.serverInfo)
	}
}

func (c *daemonConn) deliver(requestID string, msg inbound) {
	c.mu.Lock()
	ch, ok := c.waiters[requestID]
	if ok {
		delete(c.waiters, requestID)
	}
	c.mu.Unlock()
	if ok {
		ch <- msg
	}
}

// subscribe registers a broadcast channel that receives every un-correlated
// (streaming) message until unsubscribe is called or the connection dies. The
// returned channel is buffered; if a slow consumer lets it fill up, further
// broadcasts for that subscriber are dropped rather than blocking the read loop.
func (c *daemonConn) subscribe() (int, <-chan inbound) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.subSeq
	c.subSeq++
	ch := make(chan inbound, 256)
	if !c.closed {
		c.subs[id] = ch
	} else {
		close(ch) // connection already dead — hand back a closed channel
	}
	return id, ch
}

// unsubscribe removes and closes a previously registered broadcast channel.
func (c *daemonConn) unsubscribe(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.subs[id]; ok {
		delete(c.subs, id)
		close(ch)
	}
}

// publish fans a broadcast message out to all subscribers, non-blocking.
func (c *daemonConn) publish(msg inbound) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.subs) == 0 {
		c.log.Debug("dropped broadcast (no subscribers)", "type", msg.Type)
		return
	}
	for id, ch := range c.subs {
		select {
		case ch <- msg:
		default:
			c.log.Warn("stream subscriber lagging, dropping message", "subscriber", id, "type", msg.Type)
		}
	}
}

func (c *daemonConn) failAll(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	select {
	case c.fatal <- err:
	default:
	}
	for id, ch := range c.waiters {
		close(ch)
		delete(c.waiters, id)
	}
	for id, ch := range c.subs {
		close(ch)
		delete(c.subs, id)
	}
}

func (c *daemonConn) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	for id, ch := range c.subs {
		close(ch)
		delete(c.subs, id)
	}
	c.mu.Unlock()
	_ = c.ws.Close()
}

// request sends a session-wrapped RPC and waits for the correlated response.
// payloadFields are merged into the message; "type" and "requestId" are set here.
// It returns the inbound response or an error (including a daemon rpc_error).
func (c *daemonConn) request(ctx context.Context, msgType string, fields map[string]any, timeout time.Duration) (inbound, error) {
	requestID := uuid.NewString()

	message := map[string]any{"type": msgType, "requestId": requestID}
	for k, v := range fields {
		message[k] = v
	}

	ch := make(chan inbound, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return inbound{}, fmt.Errorf("connection is closed")
	}
	c.waiters[requestID] = ch
	c.mu.Unlock()

	if err := c.writeJSON(map[string]any{"type": "session", "message": message}); err != nil {
		c.mu.Lock()
		delete(c.waiters, requestID)
		c.mu.Unlock()
		return inbound{}, fmt.Errorf("send %s: %w", msgType, err)
	}

	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp, ok := <-ch:
		if !ok {
			return inbound{}, fmt.Errorf("%s failed: connection closed", msgType)
		}
		if resp.Type == "rpc_error" {
			return inbound{}, parseRPCError(resp.Payload)
		}
		return resp, nil
	case err := <-c.fatal:
		return inbound{}, fmt.Errorf("%s failed: %w", msgType, err)
	case <-ctx.Done():
		return inbound{}, ctx.Err()
	case <-timer.C:
		c.mu.Lock()
		delete(c.waiters, requestID)
		c.mu.Unlock()
		return inbound{}, fmt.Errorf("%s timed out after %s", msgType, timeout)
	}
}

// uuidString returns a fresh random UUID string.
func uuidString() string { return uuid.NewString() }

// parseRPCError turns an rpc_error payload into a Go error.
func parseRPCError(payload json.RawMessage) error {
	var p struct {
		Error       string `json:"error"`
		Code        string `json:"code"`
		RequestType string `json:"requestType"`
	}
	_ = json.Unmarshal(payload, &p)
	if p.Code != "" {
		return fmt.Errorf("daemon error (%s) on %s: %s", p.Code, p.RequestType, p.Error)
	}
	return fmt.Errorf("daemon error on %s: %s", p.RequestType, p.Error)
}
