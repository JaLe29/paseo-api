package paseo

import (
	"context"
	"encoding/json"
	"sync"
)

// StreamEvent is a single event forwarded from the daemon to a stream consumer.
// For broadcast (streaming) messages it carries the daemon's raw type and payload
// verbatim, so a consumer sees exactly what the daemon emits without this client
// having to understand every event shape. Lifecycle signals synthesized by this
// client use a reserved Type ("idle" when a turn settles, "error" on failure).
type StreamEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// StreamSession is a live attachment to an agent. It forwards the agent's
// streaming events on Events() and lets the caller send follow-up messages or
// stop the agent over the same underlying daemon connection. It is safe for the
// producer side (this package) to be driven concurrently with the caller sending
// messages; Events() must be drained by a single consumer.
type StreamSession struct {
	client  *Client
	conn    *daemonConn
	agentID string

	events    chan StreamEvent
	lifecycle chan StreamEvent // client-synthesized events (idle/error) → pump
	subID     int

	turnMu  sync.Mutex // guards `waiting`
	waiting bool       // true while a wait_for_finish is outstanding

	closeOnce sync.Once
	closed    chan struct{}
}

// Stream attaches to an existing agent and starts forwarding its live events.
// The caller is responsible for validating that the agent exists beforehand
// (so a proper HTTP status can be returned before any protocol upgrade); pass
// the canonical agent id here. Close must be called to release the connection.
func (c *Client) Stream(ctx context.Context, agentID string) (*StreamSession, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	subID, sub := conn.subscribe()
	s := &StreamSession{
		client:    c,
		conn:      conn,
		agentID:   agentID,
		events:    make(chan StreamEvent, 64),
		lifecycle: make(chan StreamEvent, 8),
		subID:     subID,
		closed:    make(chan struct{}),
	}
	go s.pump(sub)
	// Arm a wait for the current turn: if a turn is in flight the client learns
	// when it settles; if the agent is already idle it learns that immediately.
	s.armWait()
	return s, nil
}

// Events returns the channel of forwarded events. It is closed when the session
// ends (Close, daemon disconnect, or a fatal read error).
func (s *StreamSession) Events() <-chan StreamEvent { return s.events }

// SendMessage enqueues a follow-up message to the agent and arms a wait for the
// turn it starts, so the consumer will receive an "idle" event when it settles.
func (s *StreamSession) SendMessage(ctx context.Context, text string, images []Image) error {
	if err := s.conn.sendMessage(ctx, s.agentID, text, images); err != nil {
		return err
	}
	s.armWait()
	return nil
}

// Stop interrupts the agent's current run.
func (s *StreamSession) Stop(ctx context.Context) error {
	return s.conn.cancelAgent(ctx, s.agentID)
}

// Close ends the session and releases the underlying daemon connection. It is
// safe to call more than once and from any goroutine.
func (s *StreamSession) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.conn.unsubscribe(s.subID)
		s.conn.close()
	})
}

// pump is the sole owner of s.events: it merges broadcast messages (filtered to
// this agent) with client-synthesized lifecycle events and closes s.events on exit.
func (s *StreamSession) pump(sub <-chan inbound) {
	defer close(s.events)
	for {
		select {
		case <-s.closed:
			return
		case ev := <-s.lifecycle:
			if !s.deliver(ev) {
				return
			}
		case msg, ok := <-sub:
			if !ok {
				// Daemon connection died; tell the consumer and stop.
				s.deliver(StreamEvent{Type: "error", Payload: mustJSON(map[string]string{"error": "daemon connection closed"})})
				return
			}
			if s.belongs(msg.Payload) {
				if !s.deliver(StreamEvent{Type: msg.Type, Payload: msg.Payload}) {
					return
				}
			}
		}
	}
}

// deliver forwards one event to the consumer, or reports false if the session
// closed while it was blocked.
func (s *StreamSession) deliver(ev StreamEvent) bool {
	select {
	case s.events <- ev:
		return true
	case <-s.closed:
		return false
	}
}

// belongs reports whether a broadcast payload concerns this session's agent.
// Session-wide events (no agentId) and unparseable payloads are forwarded so the
// consumer never silently loses signal.
func (s *StreamSession) belongs(payload json.RawMessage) bool {
	if len(payload) == 0 {
		return true
	}
	var p struct {
		AgentID string `json:"agentId"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return true
	}
	return p.AgentID == "" || p.AgentID == s.agentID
}

// armWait starts (at most one at a time) a server-side wait for the agent to
// settle and emits an "idle" lifecycle event when it does.
func (s *StreamSession) armWait() {
	s.turnMu.Lock()
	if s.waiting {
		s.turnMu.Unlock()
		return
	}
	s.waiting = true
	s.turnMu.Unlock()

	go func() {
		defer func() {
			s.turnMu.Lock()
			s.waiting = false
			s.turnMu.Unlock()
		}()

		wr, err := s.conn.waitForFinish(context.Background(), s.agentID, s.client.opts.WaitTimeout)
		select {
		case <-s.closed:
			return // session gone; don't emit
		default:
		}
		if err != nil {
			s.pushLifecycle(StreamEvent{Type: "error", Payload: mustJSON(map[string]string{"error": err.Error()})})
			return
		}
		s.pushLifecycle(StreamEvent{
			Type: "idle",
			Payload: mustJSON(map[string]any{
				"status":      normalizeStatus(wr.Status),
				"lastMessage": wr.LastMessage,
			}),
		})
	}()
}

func (s *StreamSession) pushLifecycle(ev StreamEvent) {
	select {
	case s.lifecycle <- ev:
	case <-s.closed:
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
