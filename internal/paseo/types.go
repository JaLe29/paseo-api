// Package paseo is a native Go client for the paseo daemon (instance). It talks
// directly to ws://HOST/ws using the same protocol as the official paseo CLI —
// no CLI invocation, no subprocess spawning. It exposes the agent lifecycle
// (run, list, logs, inspect, stop, send, mode, archive, delete) plus provider
// and daemon introspection.
package paseo

import "time"

// Image is one image attached to a prompt. Data is base64 (no "data:" prefix).
type Image struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

// RunRequest is a request for a single agent run. Empty fields fall back to
// the configured defaults.
type RunRequest struct {
	Prompt      string
	Images      []Image
	Provider    string
	Model       string
	Cwd         string
	Mode        string
	Thinking    string
	WaitTimeout time.Duration
	// KeepAlive keeps the agent after the run finishes instead of deleting it,
	// so it can be inspected or streamed afterwards.
	KeepAlive bool
}

// RunResult is the outcome of an agent run.
type RunResult struct {
	// The agent id in the daemon (the agent is deleted afterwards; mostly for logging).
	AgentID string `json:"agentId"`
	// Final agent status ("completed", "error", "timeout", ...).
	Status string `json:"status"`
	// The full textual transcript of the agent (concatenated assistant messages).
	// Callers parse their answer JSON out of this, just like they did with `paseo logs`.
	Transcript string `json:"transcript"`
}
