package tui

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBoot_ResolvesProject covers §4.2 case 1: cwd is bound to a registered
// project. bootResolveScope should return single-project scope, and the
// initial list fetch should hit the project-scoped endpoint.
func TestBoot_ResolvesProject(t *testing.T) {
	var sawList bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects/resolve":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"project": map[string]any{
					"id":       7,
					"identity": "github.com/wesm/kata",
					"name":     "kata",
				},
				"workspace_root": "/tmp/x",
			})
		case "/api/v1/projects/7/issues":
			sawList = true
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	bi, err := bootResolveScope(t.Context(), c, "/tmp/x")
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, viewList, bi.view)
	sc := bi.scope
	if sc.allProjects {
		t.Fatal("expected single-project scope, got allProjects")
	}
	if sc.projectID != 7 {
		t.Fatalf("got projectID %d, want 7", sc.projectID)
	}
	if sc.projectName != "kata" {
		t.Fatalf("projectName = %q, want kata", sc.projectName)
	}
	if sc.workspace != "/tmp/x" {
		t.Fatalf("workspace = %q, want /tmp/x", sc.workspace)
	}
	if sc.homeProjectID != 7 || sc.homeProjectName != "kata" {
		t.Fatalf("home* not seeded: id=%d name=%q", sc.homeProjectID, sc.homeProjectName)
	}
	if _, err := c.ListIssues(t.Context(), sc.projectID, ListFilter{}); err != nil {
		t.Fatal(err)
	}
	if !sawList {
		t.Fatal("expected list endpoint to have been hit")
	}
}

// TestBoot_EmptyState_NoProjectsRegistered covers §4.2 case 3: cwd is
// unbound and no projects are registered. bootResolveScope should land
// on viewEmpty so Run renders an onboarding hint instead of a blank
// list. (The companion case-2 test is
// TestBoot_UnresolvedWithProjects_LandsViewProjects below, which pins
// the ≥1 project branch.)
func TestBoot_EmptyState_NoProjectsRegistered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects/resolve":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "project_not_initialized"},
			})
		case "/api/v1/projects":
			require.Equal(t, "stats", r.URL.Query().Get("include"))
			_, _ = w.Write([]byte(`{"projects":[]}`))
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	bi, err := bootResolveScope(t.Context(), c, "/tmp/empty")
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, viewEmpty, bi.view)
	if !bi.scope.empty {
		t.Fatal("expected scope.empty=true")
	}
	if bi.scope.allProjects {
		t.Fatal("did not expect allProjects")
	}
}

// TestBoot_NonResolveErrorPropagates: a 500 from /resolve should fail Run
// instead of silently downgrading. Black-screen prevention.
func TestBoot_NonResolveErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":500,"error":{"code":"internal","message":"db down"}}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())
	if _, err := bootResolveScope(t.Context(), c, "/tmp/x"); err == nil {
		t.Fatal("expected error to propagate, got nil")
	}
}

// TestInitialFilter_ZeroValueByDefault asserts the boot-time filter is
// the zero value: today there's no Options field that drives initial
// filter state. The shape is preserved so a future task can wire one up
// without changing fetchInitial.
func TestInitialFilter_ZeroValueByDefault(t *testing.T) {
	got := initialFilter(Options{})
	if got.Status != "" || got.Owner != "" || got.Author != "" ||
		got.Search != "" || len(got.Labels) != 0 {
		t.Fatalf("initialFilter = %+v, want zero value", got)
	}
}

// TestOutputIsTerminal_RejectsNonFile confirms a non-*os.File writer
// (e.g., bytes.Buffer in tests) is treated as a non-terminal so Run
// surfaces errNotATTY instead of writing alt-screen control sequences
// into a buffer that cannot honor them.
func TestOutputIsTerminal_RejectsNonFile(t *testing.T) {
	var buf bytes.Buffer
	if outputIsTerminal(&buf) {
		t.Fatal("outputIsTerminal(*bytes.Buffer) = true, want false")
	}
}

// TestRun_NonFileStdout_ReturnsNotATTY: piping into a bytes.Buffer (the
// natural test rig) must surface errNotATTY rather than panicking deep
// inside Bubble Tea's renderer.
func TestRun_NonFileStdout_ReturnsNotATTY(t *testing.T) {
	var buf bytes.Buffer
	err := Run(t.Context(), Options{Stdout: &buf})
	if !errors.Is(err, errNotATTY) {
		t.Fatalf("Run returned %v, want errNotATTY", err)
	}
}

// TestBoot_UnresolvedWithProjects_LandsViewProjects pins the new boot
// rule: an unresolved cwd plus ≥1 registered project lands on
// viewProjects, not viewEmpty. Spec §4.2.
func TestBoot_UnresolvedWithProjects_LandsViewProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects/resolve":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 404,
				"error": map[string]any{
					"code":    "project_not_initialized",
					"message": "no kata.toml",
				},
			})
		case "/api/v1/projects":
			require.Equal(t, "stats", r.URL.Query().Get("include"))
			_, _ = w.Write([]byte(`{"projects":[
				{"id":7,"identity":"github.com/wesm/kata","name":"kata",
				 "stats":{"open":3,"closed":1,"last_event_at":"2026-05-04T12:00:00.000Z"}}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())

	bi, err := bootResolveScope(t.Context(), c, "/tmp/unbound")
	require.NoError(t, err)
	assert.Equal(t, viewProjects, bi.view)
	assert.False(t, bi.scope.empty)
	assert.Zero(t, bi.scope.projectID)
	assert.False(t, bi.scope.allProjects)
	require.Len(t, bi.projects, 1, "boot fetched rows must be threaded through")
	assert.Equal(t, int64(7), bi.projects[0].ID)
	assert.Equal(t, "kata", bi.projects[0].Name)
}

// TestBuildRunModel_SeedsViewProjectsCacheFromBoot pins that when boot
// lands on viewProjects, the initial model's cache maps are populated
// from the boot fetch — no empty-then-fill flicker on the first frame.
// Spec §4.3.
func TestBuildRunModel_SeedsViewProjectsCacheFromBoot(t *testing.T) {
	t1 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	bi := bootInit{
		view:  viewProjects,
		scope: scope{},
		projects: []ProjectSummaryWithStats{
			{
				ProjectSummary: ProjectSummary{ID: 7, Identity: "github.com/wesm/kata", Name: "kata"},
				Stats:          &ProjectStatsSummary{Open: 3, Closed: 1, LastEventAt: &t1},
			},
		},
	}
	m := buildRunModel(Options{}, &Client{}, bi)
	assert.Equal(t, viewProjects, m.view)
	assert.Equal(t, "kata", m.projectsByID[7])
	assert.Equal(t, "github.com/wesm/kata", m.projectIdentByID[7])
	require.Contains(t, m.projectStats, int64(7))
	assert.Equal(t, 3, m.projectStats[7].Open)
	assert.Equal(t, 1, m.projectStats[7].Closed)
}
