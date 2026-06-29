package paseo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Agent is the daemon's agent snapshot (subset of fields we surface).
type Agent struct {
	ID                string            `json:"id"`
	Provider          string            `json:"provider"`
	Cwd               string            `json:"cwd"`
	WorkspaceID       string            `json:"workspaceId,omitempty"`
	Model             *string           `json:"model"`
	Status            string            `json:"status"`
	Title             *string           `json:"title"`
	CurrentModeID     *string           `json:"currentModeId,omitempty"`
	CreatedAt         string            `json:"createdAt,omitempty"`
	UpdatedAt         string            `json:"updatedAt,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	RequiresAttention bool              `json:"requiresAttention,omitempty"`
	AttentionReason   *string           `json:"attentionReason,omitempty"`
	ArchivedAt        *string           `json:"archivedAt,omitempty"`
	LastError         string            `json:"lastError,omitempty"`
}

// createWorkspace creates a directory-backed workspace and returns its id and
// resolved working directory.
func (c *daemonConn) createWorkspace(ctx context.Context, cwd string) (id, dir string, err error) {
	resp, err := c.request(ctx, "workspace.create.request", map[string]any{
		"source": map[string]any{"kind": "directory", "path": cwd},
	}, 60*time.Second)
	if err != nil {
		return "", "", err
	}

	var p struct {
		Workspace *struct {
			ID                 string `json:"id"`
			WorkspaceDirectory string `json:"workspaceDirectory"`
		} `json:"workspace"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return "", "", fmt.Errorf("decode workspace.create.response: %w", err)
	}
	if p.Workspace == nil {
		if p.Error != "" {
			return "", "", fmt.Errorf("workspace creation failed: %s", p.Error)
		}
		return "", "", fmt.Errorf("workspace creation returned no workspace")
	}
	dir = p.Workspace.WorkspaceDirectory
	if dir == "" {
		dir = cwd
	}
	return p.Workspace.ID, dir, nil
}

// createAgentParams holds everything needed to start an agent.
type createAgentParams struct {
	Provider    string
	Model       string
	Cwd         string
	WorkspaceID string
	Mode        string
	Thinking    string
	Title       string
	Prompt      string
	Images      []Image
}

// createAgent starts an agent. The result arrives as a "status" message with
// status "agent_created" (success) or "agent_create_failed" (failure).
func (c *daemonConn) createAgent(ctx context.Context, p createAgentParams) (Agent, error) {
	config := map[string]any{
		"provider": p.Provider,
		"cwd":      p.Cwd,
	}
	if p.Model != "" {
		config["model"] = p.Model
	}
	if p.Mode != "" {
		config["modeId"] = p.Mode
	}
	if p.Thinking != "" {
		config["thinkingOptionId"] = p.Thinking
	}
	if p.Title != "" {
		config["title"] = p.Title
	}

	fields := map[string]any{
		"config":        config,
		"initialPrompt": p.Prompt,
	}
	if p.WorkspaceID != "" {
		fields["workspaceId"] = p.WorkspaceID
	}
	if len(p.Images) > 0 {
		fields["images"] = p.Images
	}

	resp, err := c.request(ctx, "create_agent_request", fields, 60*time.Second)
	if err != nil {
		return Agent{}, err
	}

	var status struct {
		Status    string `json:"status"`
		Error     string `json:"error"`
		ErrorCode string `json:"errorCode"`
		Agent     Agent  `json:"agent"`
		AgentID   string `json:"agentId"`
	}
	if err := json.Unmarshal(resp.Payload, &status); err != nil {
		return Agent{}, fmt.Errorf("decode agent_created status: %w", err)
	}
	if status.Status == "agent_create_failed" {
		if status.ErrorCode != "" {
			return Agent{}, fmt.Errorf("agent creation failed (%s): %s", status.ErrorCode, status.Error)
		}
		return Agent{}, fmt.Errorf("agent creation failed: %s", status.Error)
	}
	if status.Agent.ID == "" {
		status.Agent.ID = status.AgentID
	}
	if status.Agent.ID == "" {
		return Agent{}, fmt.Errorf("agent creation returned no agent id")
	}
	return status.Agent, nil
}

// waitResult is the outcome of waitForFinish.
type waitResult struct {
	Status      string // "idle" (done) | "error" | "permission" | "timeout"
	Error       string
	LastMessage string
	Final       *Agent
}

// waitForFinish blocks (server-side) until the agent settles or times out.
func (c *daemonConn) waitForFinish(ctx context.Context, agentID string, timeout time.Duration) (waitResult, error) {
	fields := map[string]any{"agentId": agentID}
	if timeout > 0 {
		fields["timeoutMs"] = timeout.Milliseconds()
	}
	// Allow the server its full timeout plus a grace period before we give up locally.
	localTimeout := timeout + 5*time.Second
	if timeout <= 0 {
		localTimeout = 0 // wait indefinitely
	}

	resp, err := c.request(ctx, "wait_for_finish_request", fields, localTimeout)
	if err != nil {
		return waitResult{}, err
	}

	var p struct {
		Status      string  `json:"status"`
		Error       *string `json:"error"`
		LastMessage *string `json:"lastMessage"`
		Final       *Agent  `json:"final"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return waitResult{}, fmt.Errorf("decode wait_for_finish_response: %w", err)
	}
	res := waitResult{Status: p.Status, Final: p.Final}
	if p.Error != nil {
		res.Error = *p.Error
	}
	if p.LastMessage != nil {
		res.LastMessage = *p.LastMessage
	}
	return res, nil
}

// timelineItem is one entry's item from the agent timeline.
type timelineItem struct {
	Type    string `json:"type"`
	Text    string `json:"text"`    // user_message / assistant_message / reasoning
	Message string `json:"message"` // error
	Name    string `json:"name"`    // tool_call
}

// fetchTimeline returns all timeline items for an agent (projected view).
func (c *daemonConn) fetchTimeline(ctx context.Context, agentID string) ([]timelineItem, error) {
	resp, err := c.request(ctx, "fetch_agent_timeline_request", map[string]any{
		"agentId":    agentID,
		"direction":  "tail",
		"limit":      0,
		"projection": "projected",
	}, 30*time.Second)
	if err != nil {
		return nil, err
	}

	var p struct {
		Entries []struct {
			Item timelineItem `json:"item"`
		} `json:"entries"`
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return nil, fmt.Errorf("decode fetch_agent_timeline_response: %w", err)
	}
	if p.Error != nil && *p.Error != "" {
		return nil, fmt.Errorf("fetch timeline failed: %s", *p.Error)
	}
	items := make([]timelineItem, 0, len(p.Entries))
	for _, e := range p.Entries {
		items = append(items, e.Item)
	}
	return items, nil
}

// deleteAgent removes an agent (best effort; errors are returned but callers may ignore).
func (c *daemonConn) deleteAgent(ctx context.Context, agentID string) error {
	_, err := c.request(ctx, "delete_agent_request", map[string]any{"agentId": agentID}, 10*time.Second)
	return err
}

// listAgents returns agents. When includeArchived is true, archived agents are included.
func (c *daemonConn) listAgents(ctx context.Context, includeArchived bool) ([]Agent, error) {
	fields := map[string]any{"scope": "active"}
	if includeArchived {
		fields["filter"] = map[string]any{"includeArchived": true}
	}
	resp, err := c.request(ctx, "fetch_agents_request", fields, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var p struct {
		Entries []struct {
			Agent Agent `json:"agent"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return nil, fmt.Errorf("decode fetch_agents_response: %w", err)
	}
	agents := make([]Agent, 0, len(p.Entries))
	for _, e := range p.Entries {
		agents = append(agents, e.Agent)
	}
	return agents, nil
}

// fetchAgent resolves a single agent by id, prefix or exact title. Returns nil if not found.
func (c *daemonConn) fetchAgent(ctx context.Context, idOrTitle string) (*Agent, error) {
	resp, err := c.request(ctx, "fetch_agent_request", map[string]any{"agentId": idOrTitle}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var p struct {
		Agent *Agent  `json:"agent"`
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return nil, fmt.Errorf("decode fetch_agent_response: %w", err)
	}
	if p.Error != nil && *p.Error != "" {
		return nil, fmt.Errorf("fetch agent failed: %s", *p.Error)
	}
	return p.Agent, nil
}

// cancelAgent interrupts an agent's current run (the "stop" action).
func (c *daemonConn) cancelAgent(ctx context.Context, agentID string) error {
	_, err := c.request(ctx, "cancel_agent_request", map[string]any{"agentId": agentID}, 15*time.Second)
	return err
}

// sendMessage enqueues a message (with optional images) to a running agent.
func (c *daemonConn) sendMessage(ctx context.Context, agentID, text string, images []Image) error {
	fields := map[string]any{
		"agentId":   agentID,
		"text":      text,
		"messageId": uuidString(),
	}
	if len(images) > 0 {
		fields["images"] = images
	}
	resp, err := c.request(ctx, "send_agent_message_request", fields, 30*time.Second)
	if err != nil {
		return err
	}
	var p struct {
		Accepted bool    `json:"accepted"`
		Error    *string `json:"error"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return fmt.Errorf("decode send_agent_message_response: %w", err)
	}
	if !p.Accepted {
		msg := "message was not accepted"
		if p.Error != nil && *p.Error != "" {
			msg = *p.Error
		}
		return fmt.Errorf("send message failed: %s", msg)
	}
	return nil
}

// setMode changes an agent's mode.
func (c *daemonConn) setMode(ctx context.Context, agentID, modeID string) error {
	resp, err := c.request(ctx, "set_agent_mode_request", map[string]any{
		"agentId": agentID,
		"modeId":  modeID,
	}, 15*time.Second)
	if err != nil {
		return err
	}
	var p struct {
		Accepted bool    `json:"accepted"`
		Error    *string `json:"error"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return fmt.Errorf("decode set_agent_mode_response: %w", err)
	}
	if !p.Accepted {
		msg := "mode change was not accepted"
		if p.Error != nil && *p.Error != "" {
			msg = *p.Error
		}
		return fmt.Errorf("set mode failed: %s", msg)
	}
	return nil
}

// archiveAgent soft-deletes an agent and returns the archive timestamp.
func (c *daemonConn) archiveAgent(ctx context.Context, agentID string) (string, error) {
	resp, err := c.request(ctx, "archive_agent_request", map[string]any{"agentId": agentID}, 15*time.Second)
	if err != nil {
		return "", err
	}
	var p struct {
		ArchivedAt string `json:"archivedAt"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return "", fmt.Errorf("decode agent_archived: %w", err)
	}
	return p.ArchivedAt, nil
}

// ProviderSnapshot is one provider's availability snapshot.
type ProviderSnapshot struct {
	Provider      string `json:"provider"`
	Status        string `json:"status"`
	Enabled       bool   `json:"enabled"`
	Label         string `json:"label,omitempty"`
	Description   string `json:"description,omitempty"`
	DefaultModeID string `json:"defaultModeId,omitempty"`
	Error         string `json:"error,omitempty"`
}

// providersSnapshot lists providers known to the daemon.
func (c *daemonConn) providersSnapshot(ctx context.Context) ([]ProviderSnapshot, error) {
	resp, err := c.request(ctx, "get_providers_snapshot_request", map[string]any{}, 30*time.Second)
	if err != nil {
		return nil, err
	}
	var p struct {
		Entries []ProviderSnapshot `json:"entries"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return nil, fmt.Errorf("decode get_providers_snapshot_response: %w", err)
	}
	return p.Entries, nil
}

// Model is a provider model definition.
type Model struct {
	ID                      string `json:"id"`
	Label                   string `json:"label"`
	Description             string `json:"description,omitempty"`
	IsDefault               bool   `json:"isDefault,omitempty"`
	DefaultThinkingOptionID string `json:"defaultThinkingOptionId,omitempty"`
	ThinkingOptions         []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"thinkingOptions,omitempty"`
}

// listModels returns the models a provider offers, including thinking options.
func (c *daemonConn) listModels(ctx context.Context, provider string) ([]Model, error) {
	resp, err := c.request(ctx, "list_provider_models_request", map[string]any{"provider": provider}, 90*time.Second)
	if err != nil {
		return nil, err
	}
	var p struct {
		Models []Model `json:"models"`
		Error  *string `json:"error"`
	}
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return nil, fmt.Errorf("decode list_provider_models_response: %w", err)
	}
	if p.Error != nil && *p.Error != "" {
		return nil, fmt.Errorf("list models failed: %s", *p.Error)
	}
	return p.Models, nil
}

// DaemonStatus is the daemon's reported status.
type DaemonStatus struct {
	ServerID  string `json:"serverId"`
	Version   string `json:"version,omitempty"`
	PID       int    `json:"pid"`
	NodePath  string `json:"nodePath,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	Listen    string `json:"listen,omitempty"`
	Providers []struct {
		Provider  string `json:"provider"`
		Available bool   `json:"available"`
		Error     string `json:"error,omitempty"`
	} `json:"providers,omitempty"`
}

// daemonStatus queries the daemon's status RPC.
func (c *daemonConn) daemonStatus(ctx context.Context) (DaemonStatus, error) {
	resp, err := c.request(ctx, "daemon.get_status.request", map[string]any{}, 15*time.Second)
	if err != nil {
		return DaemonStatus{}, err
	}
	var p DaemonStatus
	if err := json.Unmarshal(resp.Payload, &p); err != nil {
		return DaemonStatus{}, fmt.Errorf("decode daemon.get_status.response: %w", err)
	}
	return p, nil
}
