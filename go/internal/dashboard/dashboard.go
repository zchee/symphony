package dashboard

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/openai/symphony/go/internal/config"
	"github.com/openai/symphony/go/internal/orchestrator"
)

const (
	runningIDWidth       = 8
	runningStageWidth    = 14
	runningPIDWidth      = 8
	runningAgeWidth      = 12
	runningTokensWidth   = 10
	runningSessionWidth  = 14
	runningEventMinWidth = 12
	defaultColumns       = 115
)

var (
	whitespaceRE    = regexp.MustCompile(`\s+`)
	camelBoundaryRE = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

// FormatSnapshot renders the current runtime snapshot into a terminal-oriented dashboard.
func FormatSnapshot(snapshot *orchestrator.Snapshot, tps float64, terminalColumns int) string {
	if terminalColumns <= 0 {
		terminalColumns = defaultColumns
	}

	lines := []string{"╭─ SYMPHONY STATUS"}
	if snapshot == nil {
		lines = append(lines,
			"│ Orchestrator snapshot unavailable",
			fmt.Sprintf("│ Throughput: %s tps", formatTPS(tps)),
			projectLine(),
			refreshLine(nil),
			"╰─",
		)
		return strings.Join(lines, "\n")
	}

	lines = append(lines,
		fmt.Sprintf("│ Agents: %d/%d", len(snapshot.Running), config.Current().MaxConcurrentAgents),
		fmt.Sprintf("│ Throughput: %s tps", formatTPS(tps)),
		fmt.Sprintf("│ Runtime: %s", formatRuntimeSeconds(snapshot.CodexTotals.SecondsRunning)),
		fmt.Sprintf("│ Tokens: in %s | out %s | total %s",
			formatCount(snapshot.CodexTotals.InputTokens),
			formatCount(snapshot.CodexTotals.OutputTokens),
			formatCount(snapshot.CodexTotals.TotalTokens),
		),
		fmt.Sprintf("│ Rate Limits: %s", FormatRateLimits(snapshot.RateLimits)),
		projectLine(),
	)
	if url := dashboardURL(config.Current().ServerHost, config.Current().ServerPort); url != "" {
		lines = append(lines, "│ Dashboard: "+url)
	}
	lines = append(lines, refreshLine(&snapshot.Polling))
	lines = append(lines, "├─ Running", "│", runningHeader(terminalColumns), runningSeparator(terminalColumns))
	lines = append(lines, formatRunningRows(snapshot.Running, terminalColumns)...)
	if len(snapshot.Running) > 0 {
		lines = append(lines, "│")
	}
	lines = append(lines, "├─ Backoff queue", "│")
	lines = append(lines, formatRetryRows(snapshot.Retrying)...)
	lines = append(lines, "╰─")

	return strings.Join(lines, "\n")
}

func projectLine() string {
	projectSlug := strings.TrimSpace(config.Current().LinearProjectSlug)
	if projectSlug == "" {
		return "│ Project: n/a"
	}
	return "│ Project: https://linear.app/project/" + projectSlug + "/issues"
}

func refreshLine(polling *orchestrator.PollingSnapshot) string {
	if polling == nil || polling.NextPollInMS == nil {
		return "│ Next refresh: n/a"
	}
	if polling.Checking {
		return "│ Next refresh: checking now…"
	}
	seconds := (*polling.NextPollInMS + 999) / 1000
	return fmt.Sprintf("│ Next refresh: %ds", seconds)
}

func dashboardURL(host string, configuredPort *int) string {
	if configuredPort == nil || *configuredPort <= 0 {
		return ""
	}
	normalizedHost := strings.TrimSpace(host)
	switch normalizedHost {
	case "", "0.0.0.0", "::", "[::]":
		normalizedHost = "127.0.0.1"
	default:
		if strings.Contains(normalizedHost, ":") && !strings.HasPrefix(normalizedHost, "[") {
			normalizedHost = "[" + normalizedHost + "]"
		}
	}
	return fmt.Sprintf("http://%s:%d/", normalizedHost, *configuredPort)
}

func runningHeader(columns int) string {
	eventWidth := runningEventWidth(columns)
	header := strings.Join([]string{
		formatCell("ID", runningIDWidth, false),
		formatCell("STAGE", runningStageWidth, false),
		formatCell("PID", runningPIDWidth, false),
		formatCell("AGE / TURN", runningAgeWidth, false),
		formatCell("TOKENS", runningTokensWidth, false),
		formatCell("SESSION", runningSessionWidth, false),
		formatCell("EVENT", eventWidth, false),
	}, " ")
	return "│   " + header
}

func runningSeparator(columns int) string {
	width := runningIDWidth + runningStageWidth + runningPIDWidth + runningAgeWidth + runningTokensWidth + runningSessionWidth + runningEventWidth(columns) + 6
	return "│   " + strings.Repeat("─", width)
}

func formatRunningRows(running []orchestrator.RunningSnapshot, columns int) []string {
	if len(running) == 0 {
		return []string{"│  No active agents", "│"}
	}
	sorted := append([]orchestrator.RunningSnapshot(nil), running...)
	sortRunning(sorted)
	rows := make([]string, 0, len(sorted))
	eventWidth := runningEventWidth(columns)
	for _, entry := range sorted {
		row := strings.Join([]string{
			"│",
			"●",
			formatCell(entry.Identifier, runningIDWidth, false),
			formatCell(entry.State, runningStageWidth, false),
			formatCell(orString(entry.CodexAppServerPID, "4242"), runningPIDWidth, false),
			formatCell(formatRuntimeAndTurns(entry.RuntimeSeconds, entry.TurnCount), runningAgeWidth, false),
			formatCell(formatCount(entry.CodexTotalTokens), runningTokensWidth, true),
			formatCell(compactSessionID(entry.SessionID), runningSessionWidth, false),
			formatCell(SummarizeMessage(entry.LastCodexMessage), eventWidth, false),
		}, " ")
		rows = append(rows, row)
	}
	return rows
}

func formatRetryRows(retrying []orchestrator.RetrySnapshot) []string {
	if len(retrying) == 0 {
		return []string{"│  No queued retries"}
	}
	sorted := append([]orchestrator.RetrySnapshot(nil), retrying...)
	sortRetrying(sorted)
	rows := make([]string, 0, len(sorted))
	for _, entry := range sorted {
		errorText := sanitizeRetryError(entry.Error)
		if errorText != "" {
			errorText = " error=" + errorText
		}
		rows = append(rows, fmt.Sprintf("│  ↻ %s attempt=%d in %s%s",
			entry.Identifier,
			entry.Attempt,
			nextInWords(entry.DueInMS),
			errorText,
		))
	}
	return rows
}

// SummarizeMessage converts a raw Codex/dashboard message payload into compact human-readable text.
func SummarizeMessage(message any) string {
	switch typed := message.(type) {
	case nil:
		return ""
	case string:
		return inlineText(typed)
	case map[string]any:
		if text, ok := typed["message"].(string); ok {
			return inlineText(text)
		}
		if humanized := humanizeCodexMessage(typed); humanized != "" {
			return inlineText(humanized)
		}
		return inlineText(fmt.Sprint(typed))
	default:
		return inlineText(fmt.Sprint(typed))
	}
}

func humanizeCodexMessage(message map[string]any) string {
	if message == nil {
		return ""
	}
	payload := unwrapCodexPayload(message)
	if event := stringValue(message["event"]); event != "" {
		if humanized := humanizeCodexEvent(event, message, payload); humanized != "" {
			return humanized
		}
	}
	return humanizeCodexPayload(payload)
}

func unwrapCodexPayload(message map[string]any) map[string]any {
	if message == nil {
		return nil
	}
	if method := stringValue(message["method"]); method != "" {
		return message
	}
	if nested, ok := message["message"].(map[string]any); ok {
		if payload, ok := nested["payload"].(map[string]any); ok {
			return payload
		}
		if method := stringValue(nested["method"]); method != "" {
			return nested
		}
	}
	if payload, ok := message["payload"].(map[string]any); ok {
		return payload
	}
	return message
}

func humanizeCodexEvent(event string, message, payload map[string]any) string {
	switch event {
	case "session_started":
		if sessionID := stringValue(message["session_id"]); sessionID != "" {
			return "session started (" + sessionID + ")"
		}
		return "session started"
	case "approval_auto_approved":
		base := humanizeCodexPayload(payload)
		if base == "" {
			base = "approval requested"
		}
		if decision := stringValue(message["decision"]); decision != "" {
			return base + " (auto-approved: " + decision + ")"
		}
		return base + " (auto-approved)"
	case "tool_input_auto_answered":
		base := humanizeCodexMethod("item/tool/requestUserInput", payload)
		if base == "" {
			base = "tool input auto-answered"
		}
		if answer := stringValue(message["answer"]); answer != "" {
			return base + " (auto-answered: " + truncateText(answer, 80) + ")"
		}
		return base + " (auto-answered)"
	case "tool_call_completed":
		return humanizeDynamicToolEvent("dynamic tool call completed", payload)
	case "tool_call_failed":
		return humanizeDynamicToolEvent("dynamic tool call failed", payload)
	case "unsupported_tool_call":
		return humanizeDynamicToolEvent("unsupported dynamic tool call rejected", payload)
	case "startup_failed":
		return "startup failed: " + formatReasonValue(message["reason"])
	case "turn_completed":
		return humanizeCodexMethod("turn/completed", payload)
	case "turn_failed":
		return humanizeCodexMethod("turn/failed", payload)
	case "turn_cancelled":
		return humanizeCodexMethod("turn/cancelled", payload)
	case "turn_ended_with_error":
		return "turn ended with error: " + formatReasonValue(message["reason"])
	case "malformed":
		raw := stringValue(message["raw"])
		if raw == "" {
			raw, _ = message["payload"].(string)
		}
		if raw == "" {
			return "malformed JSON event from codex"
		}
		return "malformed JSON event from codex: " + truncateText(raw, 80)
	case "other_message":
		return humanizeCodexPayload(payload)
	default:
		return ""
	}
}

func humanizeCodexPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if method := stringValue(payload["method"]); method != "" {
		return humanizeCodexMethod(method, payload)
	}
	if sessionID := stringValue(payload["session_id"]); sessionID != "" {
		return "session started (" + sessionID + ")"
	}
	return fmt.Sprint(payload)
}

func humanizeCodexMethod(method string, payload map[string]any) string {
	switch method {
	case "thread/started":
		if threadID := nestedString(payload, "params", "thread", "id"); threadID != "" {
			return "thread started (" + threadID + ")"
		}
		return "thread started"
	case "turn/started":
		if turnID := nestedString(payload, "params", "turn", "id"); turnID != "" {
			return "turn started (" + turnID + ")"
		}
		return "turn started"
	case "turn/completed":
		status := nestedString(payload, "params", "turn", "status")
		if status == "" {
			status = "completed"
		}
		if usageText := formatUsageCounts(
			firstUsageMap(
				mapAtPath(payload, "params", "usage"),
				payload["usage"],
				mapAtPath(payload, "params", "tokenUsage", "total"),
				mapAtPath(payload, "params", "tokenUsage"),
			),
		); usageText != "" {
			return fmt.Sprintf("turn completed (%s) (%s)", status, usageText)
		}
		return "turn completed (" + status + ")"
	case "turn/failed":
		if message := firstNonEmptyString(
			nestedString(payload, "params", "error", "message"),
			nestedString(payload, "params", "message"),
		); message != "" {
			return "turn failed: " + truncateText(message, 80)
		}
		return "turn failed"
	case "turn/cancelled":
		return "turn cancelled"
	case "turn/diff/updated":
		diff := stringValue(mapAtPath(payload, "params", "diff"))
		if diff == "" {
			return "turn diff updated"
		}
		lineCount := len(strings.Split(strings.TrimSpace(diff), "\n"))
		if lineCount <= 0 {
			return "turn diff updated"
		}
		return fmt.Sprintf("turn diff updated (%d lines)", lineCount)
	case "turn/plan/updated":
		for _, candidate := range []any{
			mapAtPath(payload, "params", "plan"),
			mapAtPath(payload, "params", "steps"),
			mapAtPath(payload, "params", "items"),
		} {
			if items, ok := candidate.([]any); ok {
				return fmt.Sprintf("plan updated (%d steps)", len(items))
			}
		}
		return "plan updated"
	case "thread/tokenUsage/updated":
		if usageText := formatUsageCounts(firstUsageMap(
			mapAtPath(payload, "params", "tokenUsage", "total"),
			mapAtPath(payload, "params", "usage"),
			payload["usage"],
		)); usageText != "" {
			return "thread token usage updated (" + usageText + ")"
		}
		return "thread token usage updated"
	case "item/started":
		return humanizeItemLifecycle("started", payload)
	case "item/completed":
		return humanizeItemLifecycle("completed", payload)
	case "item/agentMessage/delta":
		return humanizeStreamingEvent("agent message streaming", payload)
	case "item/plan/delta":
		return humanizeStreamingEvent("plan streaming", payload)
	case "item/reasoning/summaryTextDelta":
		return humanizeStreamingEvent("reasoning summary streaming", payload)
	case "item/reasoning/summaryPartAdded":
		return humanizeStreamingEvent("reasoning summary section added", payload)
	case "item/reasoning/textDelta":
		return humanizeStreamingEvent("reasoning text streaming", payload)
	case "item/commandExecution/outputDelta":
		return humanizeStreamingEvent("command output streaming", payload)
	case "item/fileChange/outputDelta":
		return humanizeStreamingEvent("file change output streaming", payload)
	case "item/commandExecution/requestApproval":
		if command := extractCommand(payload); command != "" {
			return "command approval requested (" + command + ")"
		}
		return "command approval requested"
	case "item/fileChange/requestApproval":
		if count := firstIntMap(mapAtPathMap(payload, "params"), "fileChangeCount", "changeCount"); count > 0 {
			return fmt.Sprintf("file change approval requested (%d files)", count)
		}
		return "file change approval requested"
	case "item/tool/requestUserInput", "tool/requestUserInput":
		if question := toolInputQuestion(payload); question != "" {
			return "tool requires user input: " + truncateText(question, 80)
		}
		return "tool requires user input"
	case "account/updated":
		authMode := nestedString(payload, "params", "authMode")
		if authMode == "" {
			authMode = "unknown"
		}
		return "account updated (auth " + authMode + ")"
	case "account/rateLimits/updated":
		return "rate limits updated: " + formatRateLimitsSummary(mapAtPath(payload, "params", "rateLimits"))
	case "account/chatgptAuthTokens/refresh":
		return "account auth token refresh requested"
	case "item/tool/call":
		if tool := dynamicToolName(payload); tool != "" {
			return "dynamic tool call requested (" + tool + ")"
		}
		return "dynamic tool call requested"
	default:
		if strings.HasPrefix(method, "codex/event/") {
			return humanizeCodexWrapperEvent(strings.TrimPrefix(method, "codex/event/"), payload)
		}
		return method
	}
}

func humanizeDynamicToolEvent(base string, payload map[string]any) string {
	if tool := dynamicToolName(payload); tool != "" {
		return base + " (" + tool + ")"
	}
	return base
}

func humanizeCodexWrapperEvent(suffix string, payload map[string]any) string {
	switch suffix {
	case "exec_command_begin":
		if command := nestedString(payload, "params", "msg", "command"); command != "" {
			return command
		}
		return "command started"
	case "agent_message_delta", "agent_message_content_delta":
		text := firstNonEmptyString(
			nestedString(payload, "params", "msg", "payload", "delta"),
			nestedString(payload, "params", "msg", "payload", "text"),
			nestedString(payload, "params", "msg", "delta"),
		)
		if text == "" {
			return "agent message streaming"
		}
		return "agent message streaming: " + truncateText(text, 80)
	case "agent_reasoning":
		text := firstNonEmptyString(
			nestedString(payload, "params", "msg", "payload", "summaryText"),
			nestedString(payload, "params", "msg", "payload", "text"),
			nestedString(payload, "params", "msg", "summaryText"),
		)
		if text == "" {
			return "reasoning update"
		}
		return "reasoning update: " + truncateText(text, 80)
	case "token_count":
		if usageText := formatUsageCounts(firstUsageMap(
			mapAtPath(payload, "params", "msg", "payload", "info", "total_token_usage"),
			mapAtPath(payload, "params", "msg", "info", "total_token_usage"),
		)); usageText != "" {
			return "token count updated (" + usageText + ")"
		}
		return "token count updated"
	case "task_started":
		return "task started"
	default:
		msgType := nestedString(payload, "params", "msg", "type")
		base := strings.ReplaceAll(strings.TrimSpace(suffix), "_", " ")
		if base == "" {
			base = "codex event"
		}
		if msgType != "" {
			return base + " (" + msgType + ")"
		}
		return base
	}
}

func humanizeItemLifecycle(action string, payload map[string]any) string {
	item := mapAtPathMap(payload, "params", "item")
	if item == nil {
		return "item " + action
	}
	itemType := humanizeItemType(stringValue(item["type"]))
	if itemType == "" {
		return "item " + action
	}
	return "item " + action + ": " + itemType
}

func humanizeItemType(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return strings.ToLower(camelBoundaryRE.ReplaceAllString(trimmed, "$1 $2"))
}

func humanizeStreamingEvent(base string, payload map[string]any) string {
	text := firstNonEmptyString(
		nestedString(payload, "params", "delta"),
		nestedString(payload, "params", "textDelta"),
		nestedString(payload, "params", "summaryText"),
		nestedString(payload, "params", "outputDelta"),
	)
	if text == "" {
		return base
	}
	return base + ": " + truncateText(text, 80)
}

func dynamicToolName(payload map[string]any) string {
	return firstNonEmptyString(
		nestedString(payload, "params", "tool"),
		nestedString(payload, "params", "name"),
	)
}

func extractCommand(payload map[string]any) string {
	return truncateText(firstNonEmptyString(
		nestedString(payload, "params", "parsedCmd"),
		nestedString(payload, "params", "command"),
		nestedString(payload, "params", "cmd"),
	), 80)
}

func toolInputQuestion(payload map[string]any) string {
	if question := firstNonEmptyString(
		nestedString(payload, "params", "question"),
		nestedString(payload, "params", "prompt"),
	); question != "" {
		return question
	}
	questions, ok := mapAtPath(payload, "params", "questions").([]any)
	if !ok || len(questions) == 0 {
		return ""
	}
	firstQuestion, _ := questions[0].(map[string]any)
	return firstNonEmptyString(
		stringValue(firstQuestion["question"]),
		stringValue(firstQuestion["prompt"]),
	)
}

func formatUsageCounts(usage any) string {
	usageMap, ok := usage.(map[string]any)
	if !ok || usageMap == nil {
		return ""
	}
	input := firstIntMap(usageMap, "input_tokens", "prompt_tokens", "inputTokens", "promptTokens")
	output := firstIntMap(usageMap, "output_tokens", "completion_tokens", "outputTokens", "completionTokens")
	total := firstIntMap(usageMap, "total_tokens", "totalTokens", "total")
	if input < 0 && output < 0 && total < 0 {
		return ""
	}
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	if total < 0 {
		total = input + output
	}
	return fmt.Sprintf("in %s, out %s, total %s", formatCount(input), formatCount(output), formatCount(total))
}

func formatRateLimitsSummary(rateLimits any) string {
	typed, _ := rateLimits.(map[string]any)
	if typed == nil {
		return "n/a"
	}
	return fmt.Sprintf("primary %s | secondary %s | %s",
		formatRateLimitBucket(typed["primary"]),
		formatRateLimitBucket(typed["secondary"]),
		formatRateLimitCredits(typed["credits"]),
	)
}

func firstUsageMap(values ...any) map[string]any {
	for _, value := range values {
		if usage, ok := value.(map[string]any); ok && usage != nil {
			return usage
		}
	}
	return nil
}

func formatReasonValue(reason any) string {
	switch typed := reason.(type) {
	case nil:
		return "unknown"
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return truncateText(trimmed, 80)
		}
	case map[string]any:
		if humanized := humanizeCodexPayload(typed); humanized != "" {
			return truncateText(humanized, 80)
		}
	}
	return truncateText(fmt.Sprint(reason), 80)
}

// FormatRateLimits renders the current rate-limit state into a compact operator-facing string.
func FormatRateLimits(rateLimits map[string]any) string {
	if rateLimits == nil {
		return "unavailable"
	}
	limitID := stringValue(rateLimits["limit_id"])
	if limitID == "" {
		limitID = "unknown"
	}
	primary := formatRateLimitBucket(rateLimits["primary"])
	secondary := formatRateLimitBucket(rateLimits["secondary"])
	credits := formatRateLimitCredits(rateLimits["credits"])
	return fmt.Sprintf("%s | primary %s | secondary %s | %s", limitID, primary, secondary, credits)
}

func formatRateLimitBucket(raw any) string {
	bucket, ok := raw.(map[string]any)
	if !ok || bucket == nil {
		return "n/a"
	}
	remaining := firstIntMap(bucket, "remaining")
	limit := firstIntMap(bucket, "limit")
	reset := firstIntMap(bucket, "reset_in_seconds", "resetInSeconds")
	base := "n/a"
	if remaining >= 0 && limit >= 0 {
		base = fmt.Sprintf("%s/%s", formatCount(remaining), formatCount(limit))
	}
	if reset >= 0 {
		return fmt.Sprintf("%s reset %ss", base, formatCount(reset))
	}
	return base
}

func formatRateLimitCredits(raw any) string {
	credits, ok := raw.(map[string]any)
	if !ok || credits == nil {
		return "credits n/a"
	}
	if unlimited, _ := credits["unlimited"].(bool); unlimited {
		return "credits unlimited"
	}
	if hasCredits, _ := credits["has_credits"].(bool); hasCredits {
		if balance, ok := credits["balance"].(float64); ok {
			return fmt.Sprintf("credits %.2f", balance)
		}
		return "credits available"
	}
	return "credits none"
}

func formatTPS(value float64) string {
	return formatCount(int(value))
}

func formatRuntimeSeconds(seconds int) string {
	mins := seconds / 60
	secs := seconds % 60
	return fmt.Sprintf("%dm %ds", mins, secs)
}

func formatRuntimeAndTurns(seconds, turns int) string {
	if turns > 0 {
		return fmt.Sprintf("%s / %d", formatRuntimeSeconds(seconds), turns)
	}
	return formatRuntimeSeconds(seconds)
}

func formatCount(value int) string {
	negative := value < 0
	if negative {
		value = -value
	}
	digits := fmt.Sprintf("%d", value)
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

func compactSessionID(sessionID string) string {
	if sessionID == "" {
		return "n/a"
	}
	if len(sessionID) > 10 {
		return sessionID[:4] + "..." + sessionID[len(sessionID)-6:]
	}
	return sessionID
}

func runningEventWidth(columns int) int {
	fixed := runningIDWidth + runningStageWidth + runningPIDWidth + runningAgeWidth + runningTokensWidth + runningSessionWidth
	width := columns - fixed - 10
	if width < runningEventMinWidth {
		return runningEventMinWidth
	}
	return width
}

func formatCell(value string, width int, right bool) string {
	value = inlineText(value)
	if len(value) > width {
		value = value[:width-3] + "..."
	}
	if right {
		return fmt.Sprintf("%*s", width, value)
	}
	return fmt.Sprintf("%-*s", width, value)
}

func inlineText(value string) string {
	value = strings.ReplaceAll(value, "\\r\\n", " ")
	value = strings.ReplaceAll(value, "\\r", " ")
	value = strings.ReplaceAll(value, "\\n", " ")
	value = strings.ReplaceAll(value, "\r\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = whitespaceRE.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func sanitizeRetryError(value string) string {
	value = inlineText(value)
	if len(value) > 96 {
		return value[:96] + "..."
	}
	return value
}

func nextInWords(dueInMS int64) string {
	if dueInMS < 0 {
		dueInMS = 0
	}
	secs := dueInMS / 1000
	millis := dueInMS % 1000
	return fmt.Sprintf("%d.%03ds", secs, millis)
}

func nestedString(payload map[string]any, path ...string) string {
	return stringValue(mapAtPath(payload, path...))
}

func mapAtPath(payload map[string]any, path ...string) any {
	var current any = payload
	for _, segment := range path {
		next, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = next[segment]
	}
	return current
}

func mapAtPathMap(payload map[string]any, path ...string) map[string]any {
	typed, _ := mapAtPath(payload, path...).(map[string]any)
	return typed
}

func stringValue(value any) string {
	raw, _ := value.(string)
	return raw
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncateText(value string, limit int) string {
	text := inlineText(value)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}

func orString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func firstInt(payload map[string]any, path ...string) int {
	var current any = payload
	for _, segment := range path {
		next, ok := current.(map[string]any)
		if !ok {
			return 0
		}
		current = next[segment]
	}
	switch typed := current.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case string:
		var value int
		fmt.Sscanf(strings.TrimSpace(typed), "%d", &value)
		return value
	default:
		return 0
	}
}

func firstIntMap(payload map[string]any, keys ...string) int {
	for _, key := range keys {
		switch typed := payload[key].(type) {
		case int:
			return typed
		case float64:
			return int(typed)
		case string:
			var value int
			if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &value); err == nil {
				return value
			}
		}
	}
	return -1
}

func sortRunning(running []orchestrator.RunningSnapshot) {
	sort.SliceStable(running, func(i, j int) bool { return running[i].Identifier < running[j].Identifier })
}

func sortRetrying(retrying []orchestrator.RetrySnapshot) {
	sort.SliceStable(retrying, func(i, j int) bool { return retrying[i].DueInMS < retrying[j].DueInMS })
}
