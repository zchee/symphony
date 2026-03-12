package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/symphony/go/internal/config"
	"github.com/openai/symphony/go/internal/httpui"
	"github.com/openai/symphony/go/internal/observability"
	"github.com/openai/symphony/go/internal/orchestrator"
)

// Service combines snapshot and refresh behavior for the observability API.
type Service interface {
	observability.SnapshotSource
	observability.RefreshSource
}

// Handler returns the minimal observability HTTP surface.
func Handler(service Service, snapshotTimeout time.Duration) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dashboard.css" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			_, _ = w.Write([]byte(httpui.Stylesheet()))
		case r.URL.Path == "/dashboard.css":
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		case r.URL.Path == "/" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			snapshot, err := service.Snapshot(snapshotTimeout)
			var snapshotPtr *orchestrator.Snapshot
			if err == nil {
				snapshotPtr = &snapshot
			}
			page, renderErr := httpui.RenderRoot(snapshotPtr, err, time.Now())
			if renderErr != nil {
				writeError(w, http.StatusInternalServerError, "dashboard_render_failed", "Dashboard render failed")
				return
			}
			_, _ = w.Write(page)
		case r.URL.Path == "/":
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		case r.URL.Path == "/api/v1/state" && r.Method == http.MethodGet:
			writeJSON(w, http.StatusOK, observability.StatePayload(service, snapshotTimeout, time.Now()))
		case r.URL.Path == "/api/v1/state":
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		case r.URL.Path == "/api/v1/refresh" && r.Method == http.MethodPost:
			payload, err := observability.RefreshPayload(service)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "orchestrator_unavailable", "Orchestrator is unavailable")
				return
			}
			writeJSON(w, http.StatusAccepted, payload)
		case r.URL.Path == "/api/v1/refresh":
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		case strings.HasPrefix(r.URL.Path, "/api/v1/"):
			issueIdentifier := strings.TrimPrefix(r.URL.Path, "/api/v1/")
			if issueIdentifier == "" || strings.Contains(issueIdentifier, "/") {
				writeError(w, http.StatusNotFound, "not_found", "Route not found")
				return
			}
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
				return
			}
			payload, err := observability.IssuePayload(issueIdentifier, service, snapshotTimeout, time.Now())
			if err != nil {
				writeError(w, http.StatusNotFound, "issue_not_found", "Issue not found")
				return
			}
			writeJSON(w, http.StatusOK, payload)
		default:
			writeError(w, http.StatusNotFound, "not_found", "Route not found")
		}
	})

	return mux
}

// ListenAddress returns the configured host:port pair for the observability server.
func ListenAddress() (string, error) {
	cfg := config.Current()
	if cfg.ServerPort == nil || *cfg.ServerPort < 0 {
		return "", errors.New("server disabled")
	}
	return net.JoinHostPort(cfg.ServerHost, formatPort(*cfg.ServerPort)), nil
}

func formatPort(port int) string {
	return strconv.Itoa(port)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
