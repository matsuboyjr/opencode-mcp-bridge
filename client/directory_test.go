// Regression tests added for recursive session listings in this fork.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	bridgetypes "github.com/a0970/opencodemcpbridge/types"
)

func TestDirectoryWithin(t *testing.T) {
	for _, tt := range []struct {
		root, directory string
		want            bool
	}{
		{"/projects", "/projects", true},
		{"/projects", "/projects/test", true},
		{"/projects/", "/projects/test/nested", true},
		{"/projects", "/projects-other", false},
		{"/projects", "/projects-old/test", false},
		{"/projects", "/other/projects", false},
		{"/projects", "/projects/../other", false},
		{"/projects", "/projects/test/../child", true},
		{"/", "/projects/test", true},
		{"/projects", "projects/test", false},
		{"", "/projects", false},
		{"/projects", "", false},
		{`C:\projects`, `C:\projects\test`, true},
		{`C:\projects`, `C:\projects-other`, false},
		{`C:\projects`, `D:\projects\test`, false},
	} {
		t.Run(tt.root+"->"+tt.directory, func(t *testing.T) {
			if got := directoryWithin(tt.root, tt.directory); got != tt.want {
				t.Fatalf("directoryWithin(%q, %q) = %v, want %v", tt.root, tt.directory, got, tt.want)
			}
		})
	}
}

func TestV2ListsDescendantsAcrossAllPagesAndProjects(t *testing.T) {
	pages := 0
	c, s := v2Fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/session" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Has("directory") || r.URL.Query().Has("project") {
			t.Errorf("exact-directory or project filter would hide descendants: %s", r.URL)
		}
		pages++
		switch r.URL.Query().Get("cursor") {
		case "":
			// An entirely out-of-scope page must not end pagination.
			_, _ = w.Write([]byte(`{"data":[{"id":"ses_outside","location":{"directory":"/elsewhere"}}],"cursor":{"next":"second"}}`))
		case "second":
			_, _ = w.Write([]byte(`{"data":[{"id":"ses_root","projectID":"global","location":{"directory":"/work/a"}},{"id":"ses_child","projectID":"nested-git-project","location":{"directory":"/work/a/test"}},{"id":"ses_nested","location":{"directory":"/work/a/test/sub"}},{"id":"ses_sibling","location":{"directory":"/work/another"}}],"cursor":{"next":"third"}}`))
		case "third":
			_, _ = w.Write([]byte(`{"data":[],"cursor":{}}`))
		default:
			t.Errorf("unexpected cursor %s", r.URL)
		}
	})
	defer s.Close()
	raw, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sessions []v2Session
	if err := json.Unmarshal(raw, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 || pages != 3 || sessions[0].ID != "ses_root" || sessions[1].ID != "ses_child" || sessions[2].ID != "ses_nested" {
		t.Fatalf("sessions=%s pages=%d", raw, pages)
	}
}

func TestV1ListsDescendantsUsingServerDefaultAcrossProjects(t *testing.T) {
	all := []map[string]any{}
	for i := 0; i < 100; i++ {
		all = append(all, map[string]any{"id": fmt.Sprintf("ses_outside_%d", i), "directory": "/elsewhere", "time": map[string]int{"updated": 1}})
	}
	all = append(all,
		map[string]any{"id": "ses_root", "directory": "/projects", "projectID": "global", "time": map[string]int{"updated": 1}},
		map[string]any{"id": "ses_child", "directory": "/projects/test", "projectID": "nested-git-project", "time": map[string]int{"updated": 1}},
		map[string]any{"id": "ses_sibling", "directory": "/projects-other", "time": map[string]int{"updated": 1}},
	)
	reads := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/global/health":
			_, _ = w.Write([]byte(`{"healthy":true,"version":"1.2.27"}`))
		case "/path":
			_, _ = w.Write([]byte(`{"directory":"/projects"}`))
		case "/experimental/session":
			if r.URL.Query().Get("archived") != "true" {
				t.Error("global listing must include archived sessions")
			}
			if r.URL.Query().Has("directory") || r.URL.Query().Has("cursor") {
				t.Errorf("global listing must not drop nested projects or timestamp ties: %s", r.URL)
			}
			limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
			if err != nil || limit < 1 {
				t.Errorf("invalid limit %s", r.URL)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			reads++
			if limit > len(all) {
				limit = len(all)
			}
			_ = json.NewEncoder(w).Encode(all[:limit])
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := NewWithOptions(s.URL, "", "", s.Client(), Options{})
	raw, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sessions []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 || reads != 2 || sessions[0].ID != "ses_root" || sessions[1].ID != "ses_child" {
		t.Fatalf("sessions=%s reads=%d", raw, reads)
	}
}

func TestListedDescendantDoesNotBroadenSessionOperations(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		t.Run(version, func(t *testing.T) {
			mutations := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations++
				}
				switch r.URL.Path {
				case "/global/health":
					_, _ = w.Write([]byte(`{"healthy":true,"version":"1.2.27"}`))
				case "/api/info":
					_, _ = w.Write([]byte(serverInfoV2))
				case "/api/location":
					_, _ = w.Write([]byte(locationV2))
				case "/experimental/session":
					_, _ = w.Write([]byte(`[{"id":"ses_child","directory":"/work/a/nested"}]`))
				case "/api/session":
					_, _ = w.Write([]byte(`{"data":[{"id":"ses_child","projectID":"nested","location":{"directory":"/work/a/nested"}}],"cursor":{}}`))
				case "/session/ses_child":
					_, _ = w.Write([]byte(`{"id":"ses_child","directory":"/work/a/nested"}`))
				case "/api/session/ses_child":
					_, _ = w.Write([]byte(`{"data":{"id":"ses_child","projectID":"nested","location":{"directory":"/work/a/nested"}}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer s.Close()
			c := NewWithOptions(s.URL, "", "", s.Client(), Options{APIVersion: version, Directory: "/work/a"})
			raw, err := c.Sessions(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var listed []json.RawMessage
			if json.Unmarshal(raw, &listed) != nil || len(listed) != 1 || findString(listed[0], "id") != "ses_child" {
				t.Fatalf("missing descendant: %s", raw)
			}
			if err := c.SendMessageAsync(context.Background(), bridgetypes.ReplyRequest{SessionID: "ses_child", Prompt: "must not send"}); err == nil {
				t.Fatal("listing authorized a descendant mutation")
			}
			if mutations != 0 {
				t.Fatalf("unexpected mutations: %d", mutations)
			}
		})
	}
}
