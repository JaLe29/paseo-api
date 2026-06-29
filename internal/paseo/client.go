package paseo

import (
	"log/slog"
	"strings"
	"time"
)

// Options configure the paseo daemon client.
type Options struct {
	// "IP:port" of the paseo instance (e.g. "192.168.0.3:6666").
	Host string
	// Optional daemon password.
	Password string

	// Defaults applied to a RunRequest when a field is left empty.
	DefaultProvider string
	DefaultModel    string
	DefaultCwd      string
	DefaultMode     string
	DefaultThinking string

	WaitTimeout    time.Duration
	ConnectTimeout time.Duration

	// Version this client reports to the daemon during the handshake (appVersion).
	AppVersion string

	Log *slog.Logger
}

// Client is a reusable client — it holds configuration and opens a fresh
// short-lived WS connection per operation, the same way the CLI does.
type Client struct {
	opts Options
	log  *slog.Logger
}

// New creates a client. Host must be non-empty.
func New(opts Options) *Client {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.WaitTimeout <= 0 {
		opts.WaitTimeout = 5 * time.Minute
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 15 * time.Second
	}
	if opts.AppVersion == "" {
		opts.AppVersion = "paseo-api"
	}
	return &Client{opts: opts, log: opts.Log}
}

// ValidationError is an input error (maps to HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func validationErr(msg string) error { return &ValidationError{Msg: msg} }

// applyDefaults fills request defaults from config and trims whitespace.
func (c *Client) applyDefaults(req RunRequest) (RunRequest, error) {
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		return RunRequest{}, validationErr("prompt must not be empty")
	}
	if strings.TrimSpace(req.Provider) == "" {
		req.Provider = c.opts.DefaultProvider
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = c.opts.DefaultModel
	}
	if strings.TrimSpace(req.Cwd) == "" {
		req.Cwd = c.opts.DefaultCwd
	}
	if strings.TrimSpace(req.Mode) == "" {
		req.Mode = c.opts.DefaultMode
	}
	if strings.TrimSpace(req.Thinking) == "" {
		req.Thinking = c.opts.DefaultThinking
	}
	if req.WaitTimeout <= 0 {
		req.WaitTimeout = c.opts.WaitTimeout
	}
	return req, nil
}
