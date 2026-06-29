package paseo

import (
	"context"
	"fmt"
	"strings"
)

// Run starts an agent with the given prompt (and optional images), waits for it
// to finish, collects the transcript and deletes the agent. This is the native
// equivalent of `paseo run ... && paseo logs && paseo delete` — no CLI, no
// subprocess. Each call uses its own short-lived connection to the daemon.
func (c *Client) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	req, err := c.applyDefaults(req)
	if err != nil {
		return RunResult{}, err
	}

	conn, err := c.dial(ctx)
	if err != nil {
		return RunResult{}, err
	}
	defer conn.close()

	workspaceID, runCwd, err := conn.createWorkspace(ctx, req.Cwd)
	if err != nil {
		return RunResult{}, fmt.Errorf("create workspace: %w", err)
	}

	agent, err := conn.createAgent(ctx, createAgentParams{
		Provider:    req.Provider,
		Model:       req.Model,
		Cwd:         runCwd,
		WorkspaceID: workspaceID,
		Mode:        req.Mode,
		Thinking:    req.Thinking,
		Prompt:      req.Prompt,
		Images:      req.Images,
	})
	if err != nil {
		return RunResult{}, fmt.Errorf("create agent: %w", err)
	}

	// Always try to clean the agent up afterwards (best effort).
	defer func() {
		_ = conn.deleteAgent(context.Background(), agent.ID)
	}()

	wait, err := conn.waitForFinish(ctx, agent.ID, req.WaitTimeout)
	if err != nil {
		return RunResult{}, fmt.Errorf("wait for finish: %w", err)
	}

	status := normalizeStatus(wait.Status)
	if wait.Status == "error" {
		msg := wait.Error
		if msg == "" {
			msg = "agent reported an error"
		}
		return RunResult{AgentID: agent.ID, Status: status}, fmt.Errorf("agent failed: %s", msg)
	}
	if wait.Status == "timeout" {
		return RunResult{AgentID: agent.ID, Status: status}, fmt.Errorf("agent did not finish within %s", req.WaitTimeout)
	}
	if wait.Status == "permission" {
		return RunResult{AgentID: agent.ID, Status: status}, fmt.Errorf("agent is blocked waiting for a permission decision")
	}

	transcript, err := c.collectTranscript(ctx, conn, agent.ID, wait.LastMessage)
	if err != nil {
		return RunResult{}, fmt.Errorf("collect transcript: %w", err)
	}

	return RunResult{
		AgentID:    agent.ID,
		Status:     status,
		Transcript: transcript,
	}, nil
}

// collectTranscript builds the assistant transcript. It prefers the timeline
// (full transcript, matching `paseo logs`), falling back to lastMessage.
func (c *Client) collectTranscript(ctx context.Context, conn *daemonConn, agentID, lastMessage string) (string, error) {
	items, err := conn.fetchTimeline(ctx, agentID)
	if err != nil {
		// Fall back to the last assistant message if the timeline is unavailable.
		if strings.TrimSpace(lastMessage) != "" {
			return lastMessage, nil
		}
		return "", err
	}

	var b strings.Builder
	for _, item := range items {
		if item.Type == "assistant_message" && item.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(item.Text)
		}
	}
	if b.Len() == 0 && strings.TrimSpace(lastMessage) != "" {
		return lastMessage, nil
	}
	return b.String(), nil
}

// normalizeStatus maps the daemon's wait status to a friendlier label.
// "idle" means the agent finished its turn — we report it as "completed".
func normalizeStatus(waitStatus string) string {
	if waitStatus == "idle" {
		return "completed"
	}
	return waitStatus
}

// ListAgents lists agents on the daemon.
func (c *Client) ListAgents(ctx context.Context, includeArchived bool) ([]Agent, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	return conn.listAgents(ctx, includeArchived)
}

// GetAgent resolves a single agent by id, prefix or exact title.
func (c *Client) GetAgent(ctx context.Context, idOrTitle string) (*Agent, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	return conn.fetchAgent(ctx, idOrTitle)
}

// AgentLogs returns the full transcript items of an agent as rendered text.
func (c *Client) AgentLogs(ctx context.Context, agentID string) (string, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.close()
	items, err := conn.fetchTimeline(ctx, agentID)
	if err != nil {
		return "", err
	}
	return renderTranscript(items), nil
}

// StopAgent interrupts an agent's current run.
func (c *Client) StopAgent(ctx context.Context, agentID string) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.close()
	return conn.cancelAgent(ctx, agentID)
}

// SendMessage enqueues a follow-up message (with optional images) to an agent.
func (c *Client) SendMessage(ctx context.Context, agentID, text string, images []Image) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.close()
	return conn.sendMessage(ctx, agentID, text, images)
}

// SetMode changes an agent's mode.
func (c *Client) SetMode(ctx context.Context, agentID, modeID string) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.close()
	return conn.setMode(ctx, agentID, modeID)
}

// ArchiveAgent soft-deletes an agent and returns the archive timestamp.
func (c *Client) ArchiveAgent(ctx context.Context, agentID string) (string, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return "", err
	}
	defer conn.close()
	return conn.archiveAgent(ctx, agentID)
}

// DeleteAgent hard-deletes an agent.
func (c *Client) DeleteAgent(ctx context.Context, agentID string) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.close()
	return conn.deleteAgent(ctx, agentID)
}

// Providers lists providers known to the daemon.
func (c *Client) Providers(ctx context.Context) ([]ProviderSnapshot, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	return conn.providersSnapshot(ctx)
}

// ProviderModels lists a provider's models and thinking options.
func (c *Client) ProviderModels(ctx context.Context, provider string) ([]Model, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	return conn.listModels(ctx, provider)
}

// DaemonStatus reports the daemon's health and provider availability.
func (c *Client) DaemonStatus(ctx context.Context) (DaemonStatus, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return DaemonStatus{}, err
	}
	defer conn.close()
	return conn.daemonStatus(ctx)
}

// renderTranscript produces a readable, plain-text transcript from timeline items.
func renderTranscript(items []timelineItem) string {
	var b strings.Builder
	for _, item := range items {
		switch item.Type {
		case "user_message":
			writeLine(&b, "[user] "+item.Text)
		case "assistant_message":
			writeLine(&b, item.Text)
		case "reasoning":
			writeLine(&b, "[reasoning] "+item.Text)
		case "tool_call":
			writeLine(&b, "[tool] "+item.Name)
		case "error":
			writeLine(&b, "[error] "+item.Message)
		}
	}
	return b.String()
}

func writeLine(b *strings.Builder, s string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(s)
}
