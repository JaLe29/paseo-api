package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"paseo-api/internal/paseo"
)

const maxBody = 64 << 20 // 64 MiB — base64 images can be large.

// handleHealth is a simple health check.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"host":   s.cfg.PaseoHost,
	})
}

// imageInput is the request shape for an attached image.
type imageInput struct {
	// Base64 image data (without the "data:" prefix).
	Data string `json:"data"`
	// MIME type ("image/jpeg", ...).
	MimeType string `json:"mimeType"`
}

func toImages(in []imageInput) []paseo.Image {
	out := make([]paseo.Image, 0, len(in))
	for _, img := range in {
		out = append(out, paseo.Image{Data: img.Data, MimeType: img.MimeType})
	}
	return out
}

// runRequestBody is the body of POST /run.
type runRequestBody struct {
	Prompt        string       `json:"prompt"`
	Images        []imageInput `json:"images"`
	Provider      string       `json:"provider"`
	Model         string       `json:"model"`
	Cwd           string       `json:"cwd"`
	Mode          string       `json:"mode"`
	Thinking      string       `json:"thinking"`
	WaitTimeoutMs int64        `json:"waitTimeoutMs"`
	// When true, the response includes a `json` field with all JSON objects
	// parsed out of the transcript (the last one is usually the agent's answer).
	ExtractJSON bool `json:"extractJson"`
}

// runResponseBody is the response of POST /run.
type runResponseBody struct {
	AgentID    string            `json:"agentId"`
	Status     string            `json:"status"`
	Transcript string            `json:"transcript"`
	JSON       []json.RawMessage `json:"json,omitempty"`
}

// handleRun starts an agent with a prompt (and images), waits for it to finish
// and returns the transcript. This is the direct replacement for the CLI-based
// PaseoClient.run() from ChemCheck (run → logs → delete), over HTTP.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var body runRequestBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body: "+err.Error())
		return
	}
	if body.Prompt == "" {
		writeError(w, http.StatusBadRequest, "Field 'prompt' is required.")
		return
	}

	result, err := s.client.Run(r.Context(), paseo.RunRequest{
		Prompt:      body.Prompt,
		Images:      toImages(body.Images),
		Provider:    body.Provider,
		Model:       body.Model,
		Cwd:         body.Cwd,
		Mode:        body.Mode,
		Thinking:    body.Thinking,
		WaitTimeout: time.Duration(body.WaitTimeoutMs) * time.Millisecond,
	})
	if err != nil {
		s.respondClientError(w, "run failed", err)
		return
	}

	resp := runResponseBody{
		AgentID:    result.AgentID,
		Status:     result.Status,
		Transcript: result.Transcript,
	}
	if body.ExtractJSON {
		resp.JSON = paseo.ExtractJSONObjects(result.Transcript)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListAgents lists agents. ?includeArchived=true includes archived ones.
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("includeArchived") == "true"
	agents, err := s.client.ListAgents(r.Context(), includeArchived)
	if err != nil {
		s.respondClientError(w, "list agents failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

// handleGetAgent inspects a single agent by id, prefix or exact title.
func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	agent, err := s.client.GetAgent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.respondClientError(w, "get agent failed", err)
		return
	}
	if agent == nil {
		writeError(w, http.StatusNotFound, "Agent not found.")
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

// handleAgentLogs returns the agent's transcript as plain text.
func (s *Server) handleAgentLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.client.AgentLogs(r.Context(), r.PathValue("id"))
	if err != nil {
		s.respondClientError(w, "agent logs failed", err)
		return
	}
	if r.URL.Query().Get("extractJson") == "true" {
		writeJSON(w, http.StatusOK, map[string]any{
			"transcript": logs,
			"json":       paseo.ExtractJSONObjects(logs),
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(logs))
}

// sendMessageBody is the body of POST /agents/{id}/messages.
type sendMessageBody struct {
	Text   string       `json:"text"`
	Images []imageInput `json:"images"`
}

// handleSendMessage enqueues a follow-up message to an agent.
func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var body sendMessageBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body: "+err.Error())
		return
	}
	if body.Text == "" {
		writeError(w, http.StatusBadRequest, "Field 'text' is required.")
		return
	}
	if err := s.client.SendMessage(r.Context(), r.PathValue("id"), body.Text, toImages(body.Images)); err != nil {
		s.respondClientError(w, "send message failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": true})
}

// handleStopAgent interrupts an agent's current run.
func (s *Server) handleStopAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.client.StopAgent(r.Context(), r.PathValue("id")); err != nil {
		s.respondClientError(w, "stop agent failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true})
}

// setModeBody is the body of POST /agents/{id}/mode.
type setModeBody struct {
	ModeID string `json:"modeId"`
}

// handleSetMode changes an agent's mode.
func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	var body setModeBody
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body: "+err.Error())
		return
	}
	if body.ModeID == "" {
		writeError(w, http.StatusBadRequest, "Field 'modeId' is required.")
		return
	}
	if err := s.client.SetMode(r.Context(), r.PathValue("id"), body.ModeID); err != nil {
		s.respondClientError(w, "set mode failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"modeId": body.ModeID})
}

// handleArchiveAgent soft-deletes an agent.
func (s *Server) handleArchiveAgent(w http.ResponseWriter, r *http.Request) {
	archivedAt, err := s.client.ArchiveAgent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.respondClientError(w, "archive agent failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"archivedAt": archivedAt})
}

// handleDeleteAgent hard-deletes an agent.
func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	if err := s.client.DeleteAgent(r.Context(), r.PathValue("id")); err != nil {
		s.respondClientError(w, "delete agent failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handleProviders lists providers known to the daemon.
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.client.Providers(r.Context())
	if err != nil {
		s.respondClientError(w, "list providers failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}

// handleProviderModels lists a provider's models and thinking options.
func (s *Server) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.client.ProviderModels(r.Context(), r.PathValue("provider"))
	if err != nil {
		s.respondClientError(w, "list models failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// handleDaemonStatus reports daemon health and provider availability.
func (s *Server) handleDaemonStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.client.DaemonStatus(r.Context())
	if err != nil {
		s.respondClientError(w, "daemon status failed", err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// respondClientError maps an error to the right HTTP status: input errors → 400,
// everything else (daemon problems) → 502 Bad Gateway.
func (s *Server) respondClientError(w http.ResponseWriter, context string, err error) {
	s.log.Error(context, "err", err)
	var ve *paseo.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Error())
		return
	}
	writeError(w, http.StatusBadGateway, context+": "+err.Error())
}

// decodeJSON decodes a (size-limited) JSON request body.
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(v)
}
