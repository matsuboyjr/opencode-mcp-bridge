// OpenCode V2.1.1 adapter added in this fork. Routes and types were checked
// against anomalyco/opencode v2.1.1, packages/protocol/openapi.json.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	bridgetypes "github.com/a0970/opencodemcpbridge/types"
)

type v2Session struct {
	ID string `json:"id"`
	ProjectID string `json:"projectID"`
	Location struct { Directory string `json:"directory"` } `json:"location"`
	Outcome string `json:"outcome"`
}

func encode(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }

func (c *Client) locationQuery() string {
	if c.options.Directory == "" { return "" }
	return "?" + url.Values{"location[directory]": {c.options.Directory}}.Encode()
}

func (c *Client) v2Location(ctx context.Context) (json.RawMessage, string, error) {
	raw, err := c.request(ctx, http.MethodGet, "/api/location"+c.locationQuery(), nil)
	if err != nil { return nil, "", err }
	var loc struct { Directory string `json:"directory"`; Project struct { ID string `json:"id"` } `json:"project"` }
	if json.Unmarshal(raw, &loc) != nil || loc.Directory == "" || loc.Project.ID == "" { return nil, "", errors.New("invalid V2 location response") }
	return raw, loc.Directory, nil
}

func (c *Client) ownedV2Session(ctx context.Context, id string) (json.RawMessage, error) {
	raw, err := c.v2Data(ctx, http.MethodGet, "/api/session/"+url.PathEscape(id), nil)
	if err != nil { return nil, err }
	var session v2Session
	if json.Unmarshal(raw, &session) != nil || session.ID != id || session.ProjectID == "" || session.Location.Directory == "" { return nil, errors.New("invalid V2 session response") }
	_, directory, err := c.v2Location(ctx)
	if err != nil { return nil, err }
	if session.Location.Directory != directory { return nil, fmt.Errorf("session %s belongs to directory %q, bridge is scoped to %q", id, session.Location.Directory, directory) }
	return raw, nil
}

func (c *Client) doV2(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	u, err := url.Parse(path); if err != nil { return nil, err }
	switch u.Path {
	case "/global/health":
		return c.request(ctx, http.MethodGet, "/api/info", nil)
	case "/project/current":
		raw, _, err := c.v2Location(ctx); return raw, err
	case "/mcp":
		return c.v2Data(ctx, http.MethodGet, "/api/mcp"+c.locationQuery(), nil)
	case "/provider":
		return c.v2Providers(ctx)
	case "/session/status":
		return c.v2Statuses(ctx)
	case "/session":
		_, directory, err := c.v2Location(ctx); if err != nil { return nil, err }
		if method == http.MethodPost {
			input, ok := body.(map[string]any); if !ok { return nil, errors.New("invalid session body") }
			input["location"] = map[string]string{"directory": directory}
			return c.v2Data(ctx, method, "/api/session", input)
		}
		return c.v2Sessions(ctx, directory)
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) < 2 || segments[0] != "session" { return nil, fmt.Errorf("unsupported V2 operation: %s %s", method, path) }
	id, err := url.PathUnescape(segments[1]); if err != nil { return nil, err }
	session, err := c.ownedV2Session(ctx, id); if err != nil { return nil, err }
	if len(segments) == 2 { return session, nil }
	switch segments[2] {
	case "message":
		if method == http.MethodGet {
			limit := u.Query().Get("limit"); if limit == "" { limit = "50" }
			messages, _, err := c.v2Messages(ctx, id, limit, "")
			return encode(messages), err
		}
		return c.v2Send(ctx, id, body, true)
	case "prompt_async":
		return c.v2Send(ctx, id, body, false)
	case "todo", "diff":
		return nil, fmt.Errorf("V2 does not expose the V1 %s contract; use opencode_check without detailed", segments[2])
	default:
		return nil, fmt.Errorf("unsupported V2 operation: %s %s", method, path)
	}
}

func (c *Client) v2Sessions(ctx context.Context, directory string) (json.RawMessage, error) {
	var result []json.RawMessage = []json.RawMessage{}
	cursor := ""
	seen := map[string]bool{}
	for {
		// V2's directory filter is equality-only. Scan the global cursor
		// stream and apply the directory-tree predicate locally, including
		// sessions belonging to nested projects with distinct project IDs.
		query := url.Values{"limit": {"100"}}
		if cursor != "" { query.Set("cursor", cursor) }
		raw, err := c.request(ctx, http.MethodGet, "/api/session?"+query.Encode(), nil); if err != nil { return nil, err }
		var page struct { Data []json.RawMessage `json:"data"`; Cursor struct { Next string `json:"next"` } `json:"cursor"` }
		if json.Unmarshal(raw, &page) != nil || page.Data == nil { return nil, errors.New("invalid V2 sessions page") }
		for _, item := range page.Data {
			var session v2Session
			if json.Unmarshal(item, &session) != nil || session.ID == "" || session.Location.Directory == "" { return nil, errors.New("V2 session listing returned invalid session") }
			if directoryWithin(directory, session.Location.Directory) {
				result = append(result, item)
			}
		}
		if len(page.Data) == 0 || page.Cursor.Next == "" { return encode(result), nil }
		if seen[page.Cursor.Next] { return nil, errors.New("repeated V2 session cursor") }
		cursor = page.Cursor.Next; seen[cursor] = true
	}
}

// v2Messages requests newest first and returns chronological order. Cursor is
// preserved internally so waiting can find the exact admitted prompt boundary.
func (c *Client) v2Messages(ctx context.Context, id, limit, cursor string) ([]json.RawMessage, string, error) {
	q := url.Values{"limit": {limit}}
	if cursor == "" { q.Set("order", "desc") } else { q.Set("cursor", cursor) }
	raw, err := c.request(ctx, http.MethodGet, "/api/session/"+url.PathEscape(id)+"/message?"+q.Encode(), nil)
	if err != nil { return nil, "", err }
	var page struct { Data []json.RawMessage `json:"data"`; Cursor struct { Next string `json:"next"` } `json:"cursor"` }
	if json.Unmarshal(raw, &page) != nil || page.Data == nil { return nil, "", errors.New("invalid V2 messages page") }
	for i,j := 0,len(page.Data)-1; i<j; i,j = i+1,j-1 { page.Data[i],page.Data[j] = page.Data[j],page.Data[i] }
	return page.Data, page.Cursor.Next, nil
}

func (c *Client) v2Providers(ctx context.Context) (json.RawMessage, error) {
	providers, err := c.v2Data(ctx, http.MethodGet, "/api/provider"+c.locationQuery(), nil); if err != nil { return nil, err }
	models, err := c.v2Data(ctx, http.MethodGet, "/api/model"+c.locationQuery(), nil); if err != nil { return nil, err }
	var ps []struct { ID string `json:"id"` }
	var ms []struct { ID string `json:"id"`; ProviderID string `json:"providerID"`; Enabled bool `json:"enabled"` }
	if json.Unmarshal(providers, &ps) != nil || json.Unmarshal(models, &ms) != nil { return nil, errors.New("invalid V2 provider/model list") }
	all := []any{}
	for _, p := range ps {
		available := map[string]any{}
		for _, m := range ms { if m.Enabled && m.ProviderID == p.ID { available[m.ID] = m } }
		all = append(all, map[string]any{"id": p.ID, "models": available})
	}
	return encode(map[string]any{"all": all}), nil
}

func (c *Client) v2Send(ctx context.Context, id string, body any, wait bool) (json.RawMessage, error) {
	if _, err := c.ownedV2Session(ctx, id); err != nil { return nil, err }
	input, ok := body.(map[string]any); if !ok { return nil, errors.New("invalid message body") }
	base := "/api/session/"+url.PathEscape(id)
	if agent, ok := input["agent"].(string); ok {
		if _, err := c.request(ctx, http.MethodPost, base+"/agent", map[string]string{"agent":agent}); err != nil { return nil, err }
	}
	if model, ok := input["model"].(map[string]string); ok {
		if model["providerID"] == "" { return nil, errors.New("V2 model selection requires providerID") }
		if model["modelID"] == "" {
			models, err := c.v2Data(ctx,http.MethodGet,"/api/model"+c.locationQuery(),nil); if err!=nil { return nil,err }
			var available []struct { ID string `json:"id"`; ProviderID string `json:"providerID"`; Enabled bool `json:"enabled"` }
			if json.Unmarshal(models,&available)!=nil { return nil,errors.New("invalid V2 model list") }
			for _,m:=range available { if m.ProviderID==model["providerID"] && m.Enabled { model["modelID"]=m.ID;break } }
			if model["modelID"]=="" { return nil,errors.New("no enabled V2 model for provider") }
		}
		if _, err := c.request(ctx, http.MethodPost, base+"/model", map[string]any{"model":map[string]string{"id":model["modelID"], "providerID":model["providerID"]}}); err != nil { return nil, err }
	}
	parts, ok := input["parts"].([]map[string]string); if !ok || len(parts) != 1 { return nil, errors.New("invalid prompt parts") }
	admitted, err := c.v2Data(ctx, http.MethodPost, base+"/prompt", map[string]string{"text":parts[0]["text"]}); if err != nil {
		if ctx.Err()!=nil {return nil,c.interruptV2(id,ctx.Err())}
		return nil, err
	}
	promptID := findString(admitted, "id")
	if promptID == "" { return nil, fmt.Errorf("session %s: V2 did not return the admitted prompt ID; do not resend automatically", id) }
	if !wait { return admitted, nil }
	return c.waitV2(ctx, id, promptID)
}

func (c *Client) pendingV2(ctx context.Context, id string) (string, error) {
	for _, p := range []struct { suffix, state string }{{"permission", "approval_required"}, {"form", "question_required"}} {
		raw, err := c.v2Data(ctx, http.MethodGet, "/api/session/"+url.PathEscape(id)+"/"+p.suffix, nil); if err != nil { return "", err }
		var pending []json.RawMessage
		if json.Unmarshal(raw, &pending) != nil || pending == nil { return "", errors.New("invalid V2 pending requests") }
		if len(pending) > 0 { return p.state, nil }
	}
	return "", nil
}

func (c *Client) v2Statuses(ctx context.Context) (json.RawMessage, error) {
	_, directory, err := c.v2Location(ctx); if err != nil { return nil, err }
	listed, err := c.v2Sessions(ctx, directory); if err != nil { return nil, err }
	active, err := c.v2Data(ctx, http.MethodGet, "/api/session/active", nil); if err != nil { return nil, err }
	var sessions []v2Session; var running map[string]json.RawMessage
	if json.Unmarshal(listed, &sessions) != nil || json.Unmarshal(active, &running) != nil || running == nil { return nil, errors.New("invalid V2 active sessions") }
	statuses := map[string]any{}
	for _, session := range sessions {
		state, err := c.pendingV2(ctx, session.ID); if err != nil { return nil, err }
		if state == "" {
			if _, ok := running[session.ID]; ok { state = "busy" } else if session.Outcome == "failed" || session.Outcome == "interrupted" { state = session.Outcome } else { state = "idle" }
		}
		statuses[session.ID] = map[string]string{"type":state}
	}
	return encode(statuses), nil
}

func (c *Client) waitV2(ctx context.Context, id, promptID string) (json.RawMessage, error) {
	ticker := time.NewTicker(250*time.Millisecond); defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			// Stop only this session, using a fresh bounded context because the
			// caller context has expired. Never resend an admitted prompt.
			return nil,c.interruptV2(id,err)
		}
		state, err := c.pendingV2(ctx,id); if err != nil { if ctx.Err()!=nil { continue }; return nil, err }
		if state != "" { return nil, fmt.Errorf("session %s: %s; resolve the pending request in OpenCode, then use opencode_check/conversation", id,state) }
		messages := []json.RawMessage{}
		cursor := ""; found := false; seen := map[string]bool{}
		for {
			page, next, err := c.v2Messages(ctx,id,"100",cursor); if err != nil { if ctx.Err()!=nil { break }; return nil,err }
			messages = append(page,messages...)
			for _, m := range page { if findString(m,"id")==promptID { found=true; break } }
			if found || len(page)==0 || next=="" { break }
			if seen[next] { return nil,errors.New("repeated V2 message cursor") }; seen[next]=true; cursor=next
		}
		if found {
			after := false; answer := []string{}; var last json.RawMessage
			for _, raw := range messages {
				if findString(raw,"id")==promptID { after=true; continue }; if !after { continue }
				switch findString(raw,"type") {
				case "user": return nil, fmt.Errorf("session %s received another prompt while waiting; inspect conversation",id)
				case "assistant":
					var m struct { Error json.RawMessage `json:"error"` }; _=json.Unmarshal(raw,&m)
					if len(m.Error)>0 && string(m.Error)!="null" { return nil,fmt.Errorf("session %s: provider failed: %s",id,m.Error) }
					if text:=assistantTextV2(raw); text!="" { answer=append(answer,text) }; last=raw
				case "idle":
					if findString(raw,"outcome")!="succeeded" { return nil,fmt.Errorf("session %s: execution %s",id,raw) }
					if len(answer)==0 { return nil,fmt.Errorf("session %s completed without an assistant text answer",id) }
					return encode(map[string]any{"parts":[]map[string]string{{"type":"text","text":strings.Join(answer,"\n")}}, "v2Assistant":last,"promptID":promptID}),nil
				}
			}
		}
		select { case <-ctx.Done(): case <-ticker.C: }
	}
}

func (c *Client) interruptV2(id string, cause error) error {
	ctx,cancel:=context.WithTimeout(context.Background(),3*time.Second)
	defer cancel()
	_,interruptErr:=c.ownedV2Session(ctx,id)
	if interruptErr==nil {_,interruptErr=c.request(ctx,http.MethodPost,"/api/session/"+url.PathEscape(id)+"/interrupt?resume=false",nil)}
	return fmt.Errorf("session %s: %w (interrupt error: %v)",id,cause,interruptErr)
}

func assistantTextV2(raw json.RawMessage) string {
	var message struct { Content []struct { Type string `json:"type"`; Text string `json:"text"` } `json:"content"` }
	if json.Unmarshal(raw,&message)!=nil { return "" }
	var texts []string
	for _, p:=range message.Content { if p.Type=="text" && strings.TrimSpace(p.Text)!="" { texts=append(texts,p.Text) } }
	return strings.Join(texts,"\n")
}

func (c *Client) runV2(ctx context.Context, req bridgetypes.AskRequest) (bridgetypes.RunResponse,error) {
	id,_,err:=c.CreateSession(ctx,req.Title); if err!=nil { return bridgetypes.RunResponse{},err }
	raw,err:=c.v2Send(ctx,id,messageBody(bridgetypes.ReplyRequest{SessionID:id,Prompt:req.Prompt,ProviderID:req.ProviderID,ModelID:req.ModelID,Agent:req.Agent}),true)
	if err!=nil { return bridgetypes.RunResponse{SessionID:id,Status:"failed"},fmt.Errorf("session %s: %w",id,err) }
	return bridgetypes.RunResponse{SessionID:id,Status:"completed",Message:extractMessage(raw),Raw:raw},nil
}
