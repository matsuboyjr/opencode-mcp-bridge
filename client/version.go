// OpenCode V1/V2 support added in this fork.
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
)

var ErrInvalidJSON = errors.New("OpenCode returned invalid JSON")

// Options are immutable after construction. Directory is a path on the upstream
// server, not on the bridge host. One bridge is scoped to one directory.
type Options struct {
	APIVersion string
	Directory string
}

func (c *Client) boundContext(ctx context.Context) (context.Context, context.CancelFunc) {
	duration := c.http.Timeout
	if duration <= 0 { duration = 60*time.Minute }
	return context.WithTimeout(ctx, duration)
}

// NewWithOptions enables structural API detection. New retains its historical
// V1-only behavior for applications embedding this package.
func NewWithOptions(baseURL, username, password string, hc *http.Client, options Options) *Client {
	c := New(baseURL, username, password, hc)
	if options.APIVersion == "" { options.APIVersion = "auto" }
	c.options = options
	return c
}

func (c *Client) useV2(ctx context.Context) (bool, error) {
	select {
	case c.versionLock <- struct{}{}:
		defer func() { <-c.versionLock }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if c.version != "" { return c.version == "v2", nil }
	mode := c.options.APIVersion
	if mode == "" { return false, nil }
	if mode != "auto" && mode != "v1" && mode != "v2" { return false, fmt.Errorf("invalid API version %q", mode) }
	if mode != "v2" {
		raw, err := c.request(ctx, http.MethodGet, "/global/health", nil)
		if err == nil {
			var health struct { Healthy *bool `json:"healthy"`; Version string `json:"version"` }
			if json.Unmarshal(raw, &health) == nil && health.Healthy != nil && *health.Healthy && strings.HasPrefix(health.Version, "1.") {
				c.version = "v1"; return false, nil
			}
			return false, errors.New("unrecognized V1 health response; refusing API fallback")
		}
		var he *HTTPError
		if mode == "v1" || !(errors.Is(err, ErrInvalidJSON) || (errors.As(err, &he) && he.StatusCode == http.StatusNotFound)) { return false, err }
	}
	raw, err := c.request(ctx, http.MethodGet, "/api/info", nil)
	if err != nil { return false, err }
	var info struct { Version string `json:"version"`; PID *int `json:"pid"`; URLs []string `json:"urls"`; Paths struct { Tmp string `json:"tmp"` } `json:"paths"` }
	if json.Unmarshal(raw, &info) != nil || !strings.HasPrefix(info.Version, "2.") || info.PID == nil || *info.PID < 0 || info.URLs == nil || info.Paths.Tmp == "" { return false, errors.New("unrecognized OpenCode V2 server info response") }
	c.version = "v2"
	return true, nil
}

func (c *Client) v1Path(path string) string {
	if c.options.Directory == "" { return path }
	sep := "?"; if strings.Contains(path, "?") { sep = "&" }
	return path + sep + "directory=" + url.QueryEscape(c.options.Directory)
}

func validateSessionPath(path string) error {
	u, err := url.Parse(path); if err != nil { return err }
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) < 2 || parts[0] != "session" { return nil }
	if len(parts) > 3 { return errors.New("invalid session path") }
	id, err := url.PathUnescape(parts[1]); if err != nil { return err }
	if id == "" { return errors.New("session ID is required") }
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') { return errors.New("invalid session ID") }
	}
	return nil
}

// unwrapV2 deliberately rejects HTML, missing envelopes, and null data.
func (c *Client) v2Data(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	raw, err := c.request(ctx, method, path, body)
	if err != nil { return nil, err }
	var envelope struct { Data json.RawMessage `json:"data"` }
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" { return nil, fmt.Errorf("invalid V2 data envelope from %s", path) }
	return envelope.Data, nil
}
