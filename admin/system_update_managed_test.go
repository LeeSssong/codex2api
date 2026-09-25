package admin

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagedBuildProvenanceDoesNotWaitForUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{systemUpdate: &systemUpdater{managedBuild: true, sourceRevision: "verified-commit", sourceTree: "verified-tree", fetchSourceHead: func(context.Context) (*systemSourceHead, error) {
		t.Fatal("build provenance must not fetch GitHub")
		return nil, nil
	}}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/admin/system/build", nil)
	h.GetSystemBuild(c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "verified-commit") || !strings.Contains(w.Body.String(), "verified-tree") {
		t.Fatalf("missing provenance: %s", w.Body.String())
	}
}

func TestManagedSourceTracksHloolxCommit(t *testing.T) {
	for _, tc := range []struct {
		name, head string
		update     bool
	}{
		{"current", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},
		{"changed", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &systemUpdater{managedBuild: true, currentVersion: "smartops-test", sourceRevision: "local-commit", sourceTree: "local-tree", upstreamRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", goos: "linux", goarch: "amd64", fetchSourceHead: func(context.Context) (*systemSourceHead, error) {
				return &systemSourceHead{SHA: tc.head, PublishedAt: "2026-09-25T15:56:59Z"}, nil
			}}
			got, err := u.Check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.HasUpdate != tc.update || got.Supported || got.Mode != "source_image" || got.CheckStatus != "checked" {
				t.Fatalf("unexpected managed result: %+v", got)
			}
			if got.SourceRepository != "hloolx/codex2api" || got.LatestRevision != tc.head || got.SourceRevision != "local-commit" || got.SourceTree != "local-tree" {
				t.Fatalf("missing provenance: %+v", got)
			}
		})
	}
}

func TestManagedUpdateRejectsBinaryReplacementBeforeNetwork(t *testing.T) {
	u := &systemUpdater{managedBuild: true, fetchSourceHead: func(context.Context) (*systemSourceHead, error) {
		t.Fatal("must reject before network")
		return nil, nil
	}}
	if _, err := u.PerformUpdate(context.Background()); !errors.Is(err, errSystemUpdateUnsupported) {
		t.Fatalf("got %v", err)
	}
}

func TestManagedSourceUnavailableIsUnknown(t *testing.T) {
	u := &systemUpdater{managedBuild: true, currentVersion: "smartops-test", upstreamRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	got := u.unavailableInfo()
	if got.CheckStatus != "unknown" || got.LatestVersion != "" || got.Supported || got.Warning == "" {
		t.Fatalf("unavailable source must not claim current: %+v", got)
	}
}

func TestManagedSourceRejectsMalformedCommit(t *testing.T) {
	u := &systemUpdater{managedBuild: true, upstreamRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", fetchSourceHead: func(context.Context) (*systemSourceHead, error) { return &systemSourceHead{SHA: "main"}, nil }}
	if _, err := u.Check(context.Background()); err == nil {
		t.Fatal("expected invalid commit rejection")
	}
}
