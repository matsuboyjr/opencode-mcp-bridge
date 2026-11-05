// Regression tests added for inbound Bridge API key authentication.
package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a0970/opencodemcpbridge/client"
	"github.com/a0970/opencodemcpbridge/handlers"
)

func TestAuthenticationProtectsRESTAndAllMCPPaths(t *testing.T) {
	const key = "test-bridge-api-key"
	called := false
	mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	e := NewWithOptions(nil, Options{AuthToken: key}, mcpHandler, mcpHandler)
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/"},
		{http.MethodPost, "/mcp"},
		{http.MethodGet, "/mcp"},
		{http.MethodDelete, "/mcp"},
		{http.MethodGet, "/mcp/sse"},
		{http.MethodPost, "/mcp/sse?sessionId=example"},
		{http.MethodGet, "/opencode/setup"},
		{http.MethodPost, "/opencode/ask"},
		{http.MethodPost, "/opencode/reply"},
		{http.MethodPost, "/opencode/run"},
		{http.MethodGet, "/opencode/check?sessionId=ses_test"},
		{http.MethodGet, "/opencode/conversation?sessionId=ses_test"},
		{http.MethodGet, "/opencode/sessions-overview"},
		{http.MethodGet, "/opencode/mcp-servers"},
		{http.MethodPost, "/opencode/provider-test"},
		{http.MethodPost, "/mcp?api_key=" + key},
	} {
		for _, authorization := range []string{"", "Bearer incorrect", "Basic " + key, "Bearer ", "Bearer " + key + " "} {
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			if authorization != "" {
				req.Header.Set("Authorization", authorization)
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" || called {
				t.Fatalf("%s %s auth=%q: status=%d called=%v", route.method, route.path, authorization, rec.Code, called)
			}
			if strings.Contains(rec.Body.String(), key) || strings.Contains(rec.Body.String(), "incorrect") {
				t.Fatal("authentication response disclosed credential material")
			}
		}
	}
}

func TestAuthenticationAllowsValidBearerAndRejectsDuplicateHeaders(t *testing.T) {
	const key = "test-bridge-api-key"
	called := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	})
	e := NewWithOptions(nil, Options{AuthToken: key}, h, h)
	for _, path := range []string{"/", "/mcp", "/mcp/sse"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "bearer "+key)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("valid bearer rejected on %s: %d", path, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Add("Authorization", "Bearer "+key)
	req.Header.Add("Authorization", "Bearer incorrect")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || called != 3 {
		t.Fatalf("duplicate authorization accepted: status=%d calls=%d", rec.Code, called)
	}
}

func TestAuthenticationIsOptionalAndHealthRemainsPublic(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, token := range []string{"", "test-key"} {
		e := NewWithOptions(nil, Options{AuthToken: token}, h)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
			t.Fatalf("public health failed: %d %s", rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
		want := http.StatusUnauthorized
		if token == "" {
			want = http.StatusNoContent
		}
		if rec.Code != want {
			t.Fatalf("token configured=%v status=%d want=%d", token != "", rec.Code, want)
		}
	}
}

func TestAuthenticationDoesNotForwardBridgeKeyUpstream(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "upstream-user" || password != "upstream-password" {
			t.Error("upstream did not receive its own Basic credentials")
		}
		if strings.Contains(r.Header.Get("Authorization"), "bridge-key") {
			t.Error("bridge API key forwarded upstream")
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"healthy":true,"version":"1.2.27"}`)
	}))
	defer upstream.Close()
	c := client.New(upstream.URL, "upstream-user", "upstream-password", upstream.Client())
	e := NewWithOptions(handlers.New(c), Options{AuthToken: "bridge-key"})
	req := httptest.NewRequest(http.MethodGet, "/opencode/setup", nil)
	req.Header.Set("Authorization", "Bearer bridge-key")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || calls != 2 {
		t.Fatalf("setup failed: status=%d upstream calls=%d", rec.Code, calls)
	}
}

func TestAuthenticationAllowsCORSPreflightButProtectsActualRequest(t *testing.T) {
	e := NewWithOptions(nil, Options{AuthToken: "test-key"})
	req := httptest.NewRequest(http.MethodOptions, "/opencode/ask", nil)
	req.Header.Set("Origin", "https://client.example.test")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight failed: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/opencode/ask", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://client.example.test")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("preflight bypassed actual request authentication: %d", rec.Code)
	}
}
