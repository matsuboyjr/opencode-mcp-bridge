// Per-session status normalization added for OpenCode V1/V2 support.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// ReadSessionStatus uses a targeted lookup when supported. The fallback keeps
// existing embedded clients implementing the original interface compatible.
func ReadSessionStatus(ctx context.Context, source interface {
	SessionStatuses(context.Context) (json.RawMessage, error)
}, id string) (json.RawMessage, error) {
	if targeted, ok := source.(interface {
		SessionState(context.Context, string) (json.RawMessage, error)
	}); ok {
		return targeted.SessionState(ctx, id)
	}
	raw, err := source.SessionStatuses(ctx)
	if err != nil {
		return nil, err
	}
	return statusFromV1Map(raw, id)
}

func statusFromV1Map(raw json.RawMessage, id string) (json.RawMessage, error) {
	var statuses map[string]json.RawMessage
	if json.Unmarshal(raw, &statuses) != nil || statuses == nil {
		return nil, errors.New("invalid session status map")
	}
	if state, ok := statuses[id]; ok {
		return state, nil
	}
	// V1's status endpoint omits idle sessions by contract.
	return encode(map[string]string{"type": "idle"}), nil
}

func (c *Client) SessionState(ctx context.Context, id string) (json.RawMessage, error) {
	v2, err := c.useV2(ctx)
	if err != nil {
		return nil, err
	}
	if !v2 {
		raw, err := c.SessionStatuses(ctx)
		if err != nil {
			return nil, err
		}
		return statusFromV1Map(raw, id)
	}
	raw, err := c.ownedV2Session(ctx, id)
	if err != nil {
		return nil, err
	}
	var session v2Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return nil, err
	}
	state, err := c.pendingV2(ctx, id)
	if err != nil {
		return nil, err
	}
	if state == "" {
		active, err := c.v2Data(ctx, http.MethodGet, "/api/session/active", nil)
		if err != nil {
			return nil, err
		}
		var running map[string]json.RawMessage
		if json.Unmarshal(active, &running) != nil || running == nil {
			return nil, errors.New("invalid V2 active sessions")
		}
		if _, ok := running[id]; ok {
			state = "busy"
		} else if session.Outcome == "failed" || session.Outcome == "interrupted" {
			state = session.Outcome
		} else {
			state = "idle"
		}
	}
	return encode(map[string]string{"type": state}), nil
}
