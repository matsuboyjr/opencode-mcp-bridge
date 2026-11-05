// Modified for versioned OpenCode transport and strict answer handling.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	bridgetypes "github.com/a0970/opencodemcpbridge/types"
)

const StartHint = "Please run `opencode serve` first to start the OpenCode server"

var ErrUnavailable = errors.New("cannot connect to OpenCode server")

type HTTPError struct {
	StatusCode int
	Body       string
	Path       string
}

// IsMissingProject preserves V1's optional current-project response without
// swallowing a V2 location failure (which must not report successful setup).
func IsMissingProject(err error) bool {
	var upstream *HTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusNotFound {
		return false
	}
	return upstream.Path == "" || strings.Split(upstream.Path, "?")[0] == "/project/current"
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("OpenCode returned HTTP %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
	options Options
	versionLock chan struct{}
	version string
}

func New(baseURL, username, password string, httpClient *http.Client) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), username: username, password: password, http: httpClient, versionLock:make(chan struct{},1)}
}

func (c *Client) Health(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/global/health", nil)
}

func (c *Client) Project(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/project/current", nil)
}

func (c *Client) CreateSession(ctx context.Context, title string) (string, json.RawMessage, error) {
	body := map[string]any{}
	if title != "" {
		body["title"] = title
	}
	raw, err := c.do(ctx, http.MethodPost, "/session", body)
	if err != nil {
		return "", nil, err
	}
	id := findString(raw, "id", "sessionID", "sessionId")
	if id == "" {
		return "", raw, errors.New("OpenCode create session response did not contain a session id")
	}
	return id, raw, nil
}

func (c *Client) SendMessage(ctx context.Context, req bridgetypes.ReplyRequest) (bridgetypes.MessageResponse, error) {
	ctx, cancel := c.boundContext(ctx); defer cancel()
	if c.options.APIVersion != "" {
		v2, err := c.useV2(ctx); if err != nil { return bridgetypes.MessageResponse{}, err }
		if !v2 { return c.sendV1AndWait(ctx, req) }
	}
	body := messageBody(req)
	raw, err := c.do(ctx, http.MethodPost, "/session/"+url.PathEscape(req.SessionID)+"/message", body)
	if err != nil {
		return bridgetypes.MessageResponse{}, err
	}
	message := extractMessage(raw)
	var response struct { Info struct { Error json.RawMessage `json:"error"` } `json:"info"` }
	_ = json.Unmarshal(raw, &response)
	if len(response.Info.Error) > 0 && string(response.Info.Error) != "null" { return bridgetypes.MessageResponse{}, fmt.Errorf("session %s: assistant failed: %s", req.SessionID, response.Info.Error) }
	if strings.TrimSpace(message) == "" { return bridgetypes.MessageResponse{}, fmt.Errorf("session %s returned no assistant text answer", req.SessionID) }
	return bridgetypes.MessageResponse{SessionID: req.SessionID, Message: message, Raw: raw}, nil
}

func (c *Client) Ask(ctx context.Context, req bridgetypes.AskRequest) (bridgetypes.MessageResponse, error) {
	ctx, cancel := c.boundContext(ctx); defer cancel()
	id, _, err := c.CreateSession(ctx, req.Title)
	if err != nil {
		return bridgetypes.MessageResponse{}, err
	}
	result, err := c.SendMessage(ctx, bridgetypes.ReplyRequest{SessionID: id, Prompt: req.Prompt, ProviderID: req.ProviderID, ModelID: req.ModelID, Agent: req.Agent})
	if err != nil { return result, fmt.Errorf("session %s: %w", id, err) }
	return result, nil
}

func (c *Client) Session(ctx context.Context, id string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/session/"+url.PathEscape(id), nil)
}

func (c *Client) Conversation(ctx context.Context, id string, limit int) (json.RawMessage, error) {
	path := "/session/" + url.PathEscape(id) + "/message"
	if limit > 0 {
		path += "?limit=" + fmt.Sprint(limit)
	}
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *Client) Sessions(ctx context.Context) (json.RawMessage, error) {
	ctx, cancel := c.boundContext(ctx)
	defer cancel()
	return c.do(ctx, http.MethodGet, "/session", nil)
}

func (c *Client) MCPServers(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/mcp", nil)
}

func (c *Client) SessionStatuses(ctx context.Context) (json.RawMessage, error) {
	raw, err := c.do(ctx, http.MethodGet, "/session/status", nil)
	if err != nil || c.options.APIVersion == "" { return raw, err }
	v2, err := c.useV2(ctx); if err != nil || v2 { return raw, err }
	return c.v1PendingStatuses(ctx, raw)
}

func (c *Client) Todos(ctx context.Context, id string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/session/"+url.PathEscape(id)+"/todo", nil)
}

func (c *Client) Diff(ctx context.Context, id string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/session/"+url.PathEscape(id)+"/diff", nil)
}

func (c *Client) Providers(ctx context.Context) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/provider", nil)
}

func (c *Client) SendMessageAsync(ctx context.Context, req bridgetypes.ReplyRequest) error {
	body := messageBody(req)
	_, err := c.do(ctx, http.MethodPost, "/session/"+url.PathEscape(req.SessionID)+"/prompt_async", body)
	return err
}

func (c *Client) Run(ctx context.Context, req bridgetypes.AskRequest) (bridgetypes.RunResponse, error) {
	ctx, cancel := c.boundContext(ctx); defer cancel()
	v2, err := c.useV2(ctx)
	if err != nil { return bridgetypes.RunResponse{}, err }
	if v2 { return c.runV2(ctx, req) }
	id, _, err := c.CreateSession(ctx, req.Title)
	if err != nil {
		return bridgetypes.RunResponse{}, err
	}
	reply := bridgetypes.ReplyRequest{SessionID: id, Prompt: req.Prompt, ProviderID: req.ProviderID, ModelID: req.ModelID, Agent: req.Agent}
	if err := c.SendMessageAsync(ctx, reply); err != nil {
		if ctx.Err()!=nil { return bridgetypes.RunResponse{SessionID:id,Status:"failed"}, c.abortV1(id,ctx.Err()) }
		return bridgetypes.RunResponse{SessionID: id, Status: "failed"}, err
	}
	return c.waitV1(ctx, id, nil)
}

func (c *Client) waitV1(ctx context.Context, id string, previous map[string]bool) (bridgetypes.RunResponse, error) {

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			state:="timeout"; if errors.Is(ctx.Err(),context.Canceled) {state="cancelled"}
			return bridgetypes.RunResponse{SessionID:id,Status:state},c.abortV1(id,ctx.Err())
		case <-ticker.C:
			statuses, err := c.SessionStatuses(ctx)
			if err != nil {
				if ctx.Err() != nil { continue }
				return bridgetypes.RunResponse{SessionID: id, Status: "failed"}, err
			}
			var states map[string]struct { Type string `json:"type"` }
			if json.Unmarshal(statuses, &states) != nil { return bridgetypes.RunResponse{SessionID:id,Status:"failed"}, errors.New("invalid V1 session statuses") }
			if state:=states[id].Type; state=="approval_required" || state=="question_required" || state=="failed" { return bridgetypes.RunResponse{SessionID:id,Status:state},fmt.Errorf("session %s: %s",id,state) }
			if sessionBusy(statuses, id) {
				continue
			}
			conversation, err := c.Conversation(ctx, id, 100)
			if err != nil {
				if ctx.Err()!=nil {continue}
				return bridgetypes.RunResponse{SessionID: id, Status: "failed"}, err
			}
			message, answerErr := v1Answer(conversation, previous, c.options.APIVersion != "")
			if answerErr != nil { return bridgetypes.RunResponse{SessionID:id,Status:"failed"}, fmt.Errorf("session %s: %w",id,answerErr) }
			if message == "" {
				continue
			}
			return bridgetypes.RunResponse{SessionID: id, Status: "completed", Message: message, Raw: conversation}, nil
		}
	}
}

func messageBody(req bridgetypes.ReplyRequest) map[string]any {
	body := map[string]any{"parts": []map[string]string{{"type": "text", "text": req.Prompt}}}
	if req.ProviderID != "" || req.ModelID != "" {
		body["model"] = map[string]string{"providerID": req.ProviderID, "modelID": req.ModelID}
	}
	if req.Agent != "" {
		body["agent"] = req.Agent
	}
	return body
}

func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	if err := validateSessionPath(path); err != nil { return nil, err }
	v2, err := c.useV2(ctx)
	if err != nil { return nil, err }
	if v2 { return c.doV2(ctx, method, path, body) }
	if c.options.APIVersion != "" {
		if err := c.guardV1(ctx, path); err != nil { return nil, err }
		if method == http.MethodGet && path == "/session" { return c.v1Sessions(ctx) }
	}
	return c.request(ctx, method, c.v1Path(path), body)
}

func (c *Client) request(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(data)), Path:path}
	}
	if len(data) == 0 {
		return json.RawMessage("null"), nil
	}
	if !json.Valid(data) {
		return nil, ErrInvalidJSON
	}
	return data, nil
}

func findString(raw json.RawMessage, keys ...string) string {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range keys {
		if text, ok := value[key].(string); ok {
			return text
		}
	}
	return ""
}

func extractMessage(raw json.RawMessage) string {
	var legacy string
	if json.Unmarshal(raw, &legacy) == nil { return legacy }
	var message struct { Parts []struct { Type string `json:"type"`; Text string `json:"text"` } `json:"parts"` }
	if json.Unmarshal(raw, &message) != nil { return "" }
	var text []string
	for _, part := range message.Parts { if part.Type == "text" && strings.TrimSpace(part.Text) != "" { text = append(text, part.Text) } }
	return strings.Join(text, "\n")
}

func sessionBusy(raw json.RawMessage, id string) bool {
	var statuses map[string]struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &statuses) != nil {
		return true
	}
	status, ok := statuses[id]
	return ok && status.Type != "idle"
}

func lastAssistantMessage(raw json.RawMessage) string {
	var messages []struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if json.Unmarshal(raw, &messages) != nil {
		return ""
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Info.Role != "assistant" {
			continue
		}
		var parts []string
		for _, part := range messages[i].Parts {
			if part.Type == "text" && part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
}
