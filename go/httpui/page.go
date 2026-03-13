package httpui

import (
	"bytes"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"time"

	"github.com/openai/symphony/go/config"
	"github.com/openai/symphony/go/dashboard"
	"github.com/openai/symphony/go/orchestrator"
)

type pageData struct {
	Live           bool
	GeneratedAt    string
	CSSURL         string
	StateURL       string
	RefreshURL     string
	RunningCount   int
	RetryingCount  int
	TotalTokens    string
	Runtime        string
	RateLimits     string
	ProjectURL     string
	ProjectLabel   string
	NextRefresh    string
	Running        []runningRow
	Retrying       []retryRow
	UnavailableMsg string
}

type runningRow struct {
	Identifier      string
	State           string
	SessionID       string
	DetailURL       string
	AppServerPID    string
	RuntimeAndTurns string
	TotalTokens     string
	LastMessage     string
}

type retryRow struct {
	Identifier string
	Attempt    int
	DueIn      string
	Error      string
	DetailURL  string
}

var pageTemplate = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta http-equiv="refresh" content="1">
  <title>Symphony Dashboard</title>
  <link rel="stylesheet" href="{{.CSSURL}}">
</head>
<body>
  <div class="shell">
    <header class="masthead">
      <div>
        <p class="eyebrow">Operator Surface</p>
        <h1>Symphony Dashboard</h1>
        <p class="lede">{{if .Live}}Live runtime snapshot rendered from the active Go service.{{else}}Snapshot unavailable. The JSON API may still report more detail.{{end}}</p>
      </div>
      <div class="masthead-actions">
        <a class="ghost" href="{{.StateURL}}">Raw State JSON</a>
        <button id="refresh-button" class="primary" type="button" data-refresh-url="{{.RefreshURL}}">Queue Refresh</button>
      </div>
    </header>

    <section class="summary-grid">
      <article class="stat-card">
        <h2>Agents</h2>
        <p class="stat-value">{{.RunningCount}}</p>
        <p class="stat-meta">running</p>
      </article>
      <article class="stat-card">
        <h2>Retries</h2>
        <p class="stat-value">{{.RetryingCount}}</p>
        <p class="stat-meta">queued</p>
      </article>
      <article class="stat-card">
        <h2>Tokens</h2>
        <p class="stat-value">{{.TotalTokens}}</p>
        <p class="stat-meta">total</p>
      </article>
      <article class="stat-card">
        <h2>Runtime</h2>
        <p class="stat-value">{{.Runtime}}</p>
        <p class="stat-meta">aggregate</p>
      </article>
      <article class="stat-card wide">
        <h2>Rate Limits</h2>
        <p class="stat-body">{{.RateLimits}}</p>
      </article>
      <article class="stat-card wide">
        <h2>Project</h2>
        <p class="stat-body">{{if .ProjectURL}}<a href="{{.ProjectURL}}">{{.ProjectLabel}}</a>{{else}}{{.ProjectLabel}}{{end}}</p>
        <p class="stat-meta">Next refresh: {{.NextRefresh}}</p>
      </article>
    </section>

    {{if not .Live}}
    <section class="panel unavailable">
      <h2>Snapshot Unavailable</h2>
      <p>{{.UnavailableMsg}}</p>
    </section>
    {{end}}

    <section class="panel">
      <div class="panel-header">
        <h2>Running Sessions</h2>
        <span class="badge">{{.RunningCount}}</span>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Issue</th>
              <th>State</th>
              <th>Session</th>
              <th>PID</th>
              <th>Age / Turn</th>
              <th>Tokens</th>
              <th>Last Event</th>
            </tr>
          </thead>
          <tbody>
            {{if .Running}}
              {{range .Running}}
              <tr>
                <td><a href="{{.DetailURL}}">{{.Identifier}}</a></td>
                <td>{{.State}}</td>
                <td>{{.SessionID}}</td>
                <td>{{.AppServerPID}}</td>
                <td>{{.RuntimeAndTurns}}</td>
                <td>{{.TotalTokens}}</td>
                <td>{{.LastMessage}}</td>
              </tr>
              {{end}}
            {{else}}
              <tr><td colspan="7" class="empty">No active agents</td></tr>
            {{end}}
          </tbody>
        </table>
      </div>
    </section>

    <section class="panel">
      <div class="panel-header">
        <h2>Retry Queue</h2>
        <span class="badge">{{.RetryingCount}}</span>
      </div>
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Issue</th>
              <th>Attempt</th>
              <th>Retry In</th>
              <th>Last Error</th>
            </tr>
          </thead>
          <tbody>
            {{if .Retrying}}
              {{range .Retrying}}
              <tr>
                <td><a href="{{.DetailURL}}">{{.Identifier}}</a></td>
                <td>{{.Attempt}}</td>
                <td>{{.DueIn}}</td>
                <td>{{.Error}}</td>
              </tr>
              {{end}}
            {{else}}
              <tr><td colspan="4" class="empty">No queued retries</td></tr>
            {{end}}
          </tbody>
        </table>
      </div>
    </section>
  </div>
  <script>
    const button = document.getElementById("refresh-button");
    if (button) {
      button.addEventListener("click", async () => {
        button.disabled = true;
        try {
          await fetch(button.dataset.refreshUrl, { method: "POST" });
          window.location.reload();
        } finally {
          button.disabled = false;
        }
      });
    }
  </script>
</body>
</html>`))

const stylesheet = `:root {
  --bg: linear-gradient(180deg, #f4efe4 0%, #ebe2d2 48%, #e6d7c1 100%);
  --panel: rgba(255, 252, 247, 0.88);
  --panel-border: rgba(72, 56, 38, 0.16);
  --text: #21180e;
  --muted: #64513b;
  --accent: #8a2e1f;
  --accent-soft: #efe0dc;
  --shadow: 0 18px 50px rgba(61, 42, 17, 0.12);
  --radius: 22px;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  font-family: "Avenir Next", "Segoe UI", sans-serif;
  color: var(--text);
  background: var(--bg);
}

a { color: inherit; }

.shell {
  max-width: 1320px;
  margin: 0 auto;
  padding: 28px 20px 56px;
}

.masthead {
  display: flex;
  gap: 18px;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 24px;
}

.eyebrow {
  margin: 0 0 10px;
  text-transform: uppercase;
  letter-spacing: 0.18em;
  font-size: 12px;
  color: var(--muted);
}

h1, h2 {
  margin: 0;
  font-family: "Iowan Old Style", "Palatino Linotype", Georgia, serif;
}

h1 {
  font-size: clamp(34px, 5vw, 56px);
  line-height: 0.95;
}

.lede {
  max-width: 60ch;
  margin: 12px 0 0;
  color: var(--muted);
}

.masthead-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.primary, .ghost {
  border-radius: 999px;
  border: 1px solid transparent;
  padding: 11px 16px;
  font: inherit;
  text-decoration: none;
  cursor: pointer;
}

.primary {
  background: var(--accent);
  color: #fff9f6;
  box-shadow: var(--shadow);
}

.ghost {
  background: rgba(255, 255, 255, 0.55);
  border-color: var(--panel-border);
}

.summary-grid {
  display: grid;
  gap: 14px;
  grid-template-columns: repeat(12, minmax(0, 1fr));
  margin-bottom: 18px;
}

.stat-card, .panel {
  background: var(--panel);
  border: 1px solid var(--panel-border);
  border-radius: var(--radius);
  box-shadow: var(--shadow);
  backdrop-filter: blur(14px);
}

.stat-card {
  grid-column: span 3;
  padding: 18px 18px 16px;
}

.stat-card.wide { grid-column: span 6; }

.stat-card h2 {
  font-size: 16px;
  margin-bottom: 14px;
}

.stat-value {
  margin: 0;
  font-size: clamp(30px, 4vw, 44px);
  line-height: 1;
}

.stat-body, .stat-meta {
  margin: 10px 0 0;
  color: var(--muted);
}

.panel {
  padding: 18px;
  margin-top: 18px;
}

.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.badge {
  border-radius: 999px;
  background: var(--accent-soft);
  color: var(--accent);
  padding: 4px 10px;
  font-size: 13px;
}

.table-wrap {
  overflow-x: auto;
}

table {
  width: 100%;
  border-collapse: collapse;
  min-width: 760px;
}

th, td {
  text-align: left;
  padding: 12px 10px;
  border-top: 1px solid rgba(72, 56, 38, 0.1);
  vertical-align: top;
}

thead th {
  border-top: none;
  color: var(--muted);
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.12em;
}

tbody td {
  font-size: 14px;
}

.empty {
  color: var(--muted);
  text-align: center;
}

.unavailable {
  border-color: rgba(138, 46, 31, 0.24);
}

@media (max-width: 960px) {
  .masthead {
    flex-direction: column;
  }

  .stat-card, .stat-card.wide {
    grid-column: span 12;
  }
}`

// RenderRoot returns the operator-facing HTML dashboard for the current snapshot.
func RenderRoot(snapshot *orchestrator.Snapshot, snapshotErr error, now time.Time) ([]byte, error) {
	data := pageData{
		Live:        snapshot != nil && snapshotErr == nil,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		CSSURL:      "/dashboard.css",
		StateURL:    "/api/v1/state",
		RefreshURL:  "/api/v1/refresh",
		ProjectURL:  projectURL(),
		ProjectLabel: func() string {
			if slug := strings.TrimSpace(config.Current().LinearProjectSlug); slug != "" {
				return slug
			}
			return "n/a"
		}(),
	}

	if snapshot == nil || snapshotErr != nil {
		data.UnavailableMsg = "The runtime snapshot is currently unavailable. Use the JSON API for direct diagnostics."
		data.TotalTokens = "0"
		data.Runtime = "0m 0s"
		data.RateLimits = "unavailable"
		data.NextRefresh = "n/a"
	} else {
		data.RunningCount = len(snapshot.Running)
		data.RetryingCount = len(snapshot.Retrying)
		data.TotalTokens = formatCount(snapshot.CodexTotals.TotalTokens)
		data.Runtime = formatRuntimeSeconds(snapshot.CodexTotals.SecondsRunning)
		data.RateLimits = dashboard.FormatRateLimits(snapshot.RateLimits)
		data.NextRefresh = nextRefresh(snapshot.Polling)
		data.Running = runningRows(snapshot.Running)
		data.Retrying = retryRows(snapshot.Retrying)
	}

	var out bytes.Buffer
	if err := pageTemplate.Execute(&out, data); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Stylesheet returns the CSS served by the HTML dashboard.
func Stylesheet() string {
	return stylesheet
}

func projectURL() string {
	projectSlug := strings.TrimSpace(config.Current().LinearProjectSlug)
	if projectSlug == "" {
		return ""
	}
	return "https://linear.app/project/" + projectSlug + "/issues"
}

func nextRefresh(polling orchestrator.PollingSnapshot) string {
	if polling.NextPollInMS == nil {
		return "n/a"
	}
	if polling.Checking {
		return "checking now..."
	}
	seconds := (*polling.NextPollInMS + 999) / 1000
	return fmt.Sprintf("%ds", seconds)
}

func runningRows(entries []orchestrator.RunningSnapshot) []runningRow {
	rows := make([]runningRow, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, runningRow{
			Identifier:      entry.Identifier,
			State:           entry.State,
			SessionID:       defaultString(entry.SessionID, "n/a"),
			DetailURL:       "/api/v1/" + entry.Identifier,
			AppServerPID:    defaultString(entry.CodexAppServerPID, "n/a"),
			RuntimeAndTurns: fmt.Sprintf("%s / %d", formatRuntimeSeconds(entry.RuntimeSeconds), entry.TurnCount),
			TotalTokens:     formatCount(entry.CodexTotalTokens),
			LastMessage:     dashboard.SummarizeMessage(entry.LastCodexMessage),
		})
	}
	return rows
}

func retryRows(entries []orchestrator.RetrySnapshot) []retryRow {
	rows := make([]retryRow, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, retryRow{
			Identifier: entry.Identifier,
			Attempt:    entry.Attempt,
			DueIn:      dueIn(entry.DueInMS),
			Error:      defaultString(entry.Error, "n/a"),
			DetailURL:  "/api/v1/" + entry.Identifier,
		})
	}
	return rows
}

func dueIn(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%d.%03ds", ms/1000, ms%1000)
}

func formatRuntimeSeconds(seconds int) string {
	mins := seconds / 60
	secs := seconds % 60
	return fmt.Sprintf("%dm %ds", mins, secs)
}

func formatCount(value int) string {
	negative := value < 0
	if negative {
		value = -value
	}
	digits := strconv.Itoa(value)
	var out []byte
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(r))
	}
	if negative {
		return "-" + string(out)
	}
	return string(out)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
