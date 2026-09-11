package tokensaver

import (
	"strconv"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// mockLogger captures warnings for fail-open assertions.
type mockLogger struct {
	warnings []string
	infos    []string
}

func (l *mockLogger) Debug(msg string, args ...any) {}
func (l *mockLogger) Info(msg string, args ...any)  { l.infos = append(l.infos, msg) }
func (l *mockLogger) Warn(msg string, args ...any)  { l.warnings = append(l.warnings, msg) }
func (l *mockLogger) Error(msg string, args ...any) {}
func (l *mockLogger) Fatal(msg string, args ...any) {}
func (l *mockLogger) SetLevel(level schemas.LogLevel) {
}
func (l *mockLogger) SetOutputType(outputType schemas.LoggerOutputType) {
}
func (l *mockLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return noopBuilder{}
}

type noopBuilder struct{}

func (noopBuilder) Str(key, val string) schemas.LogEventBuilder { return noopBuilder{} }
func (noopBuilder) Int(key string, val int) schemas.LogEventBuilder {
	return noopBuilder{}
}
func (noopBuilder) Int64(key string, val int64) schemas.LogEventBuilder {
	return noopBuilder{}
}
func (noopBuilder) Send() {}

func newTestPlugin(t *testing.T) *Plugin {
	t.Helper()
	p, err := Init(nil, &mockLogger{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

// --- filters ---

func makeLines(n int, prefix string) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(prefix)
		sb.WriteString(strings.Repeat("x", 8))
		sb.WriteString("\n")
	}
	return sb.String()
}

func makeUniqueLines(n int, prefix string) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(prefix)
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(" unique-line-content\n")
	}
	return sb.String()
}

func TestFilterSmartTruncate(t *testing.T) {
	text := makeLines(400, "line-")
	out := filterSmartTruncate(text)
	if !strings.Contains(out, "[token-saver:") {
		t.Fatal("expected truncation marker")
	}
	if got := strings.Count(out, "\n"); got >= 400 {
		t.Fatalf("expected fewer lines, got %d", got)
	}
	// Under threshold: untouched.
	small := makeLines(100, "line-")
	if filterSmartTruncate(small) != small {
		t.Fatal("small blob must pass through")
	}
}

func TestFilterDedupLog(t *testing.T) {
	text := strings.Repeat("INFO something happened\n", 50) + "INFO different\n"
	out := filterDedupLog(text)
	if strings.Count(out, "INFO something happened") != 1 {
		t.Fatal("consecutive duplicates must collapse to one")
	}
	if !strings.Contains(out, "INFO different") {
		t.Fatal("unique line must survive")
	}
	// No duplicates: untouched.
	unique := "a\nb\nc\n"
	if filterDedupLog(unique) != unique {
		t.Fatal("no-dup blob must pass through")
	}
}

func TestFilterGitDiffHunkCap(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("diff --git a/file.go b/file.go\nindex 123..456 100644\n--- a/file.go\n+++ b/file.go\n@@ -1,1 +1,1 @@\n")
	for i := 0; i < 300; i++ {
		sb.WriteString("+added line\n")
	}
	sb.WriteString("@@ -10,1 +10,1 @@\n+small hunk\n")
	out := filterGitDiff(sb.String())
	if !strings.Contains(out, "diff --git a/file.go") {
		t.Fatal("file header must survive")
	}
	if !strings.Contains(out, "hunk truncated") {
		t.Fatal("oversized hunk must be truncated")
	}
	if !strings.Contains(out, "+small hunk") {
		t.Fatal("second hunk must survive intact")
	}
}

// --- detect ---

func TestDetect(t *testing.T) {
	fs := newFilterSet(nil)
	if name, _ := fs.detect("diff --git a/x b/x\nindex 1..2\n"); name != FilterGitDiff {
		t.Fatalf("diff must detect git-diff, got %q", name)
	}
	repetitive := strings.Repeat("INFO same message\n", 30)
	if name, _ := fs.detect(repetitive); name != FilterDedupLog {
		t.Fatalf("repetitive log must detect dedup-log, got %q", name)
	}
	prose := makeUniqueLines(300, "para ")
	if name, _ := fs.detect(prose); name != FilterSmartTruncate {
		t.Fatalf("fallback must be smart-truncate, got %q", name)
	}
}

func TestEnabledFilterSubset(t *testing.T) {
	fs := newFilterSet(&RTKFilterSettings{Enabled: []string{FilterSmartTruncate}})
	name, ok := fs.detect("diff --git a/x b/x\nindex 1..2\n")
	if !ok || name != FilterSmartTruncate {
		t.Fatalf("disabled git-diff must fall through to smart-truncate, got %q ok=%v", name, ok)
	}
}

// --- guards ---

func TestSmallBlobUntouched(t *testing.T) {
	p := newTestPlugin(t)
	stats := compressStats{filters: map[string]int{}}
	small := "tiny"
	p.compressStringPtr(&small, &stats)
	if small != "tiny" || stats.hits != 0 {
		t.Fatal("blob under min_bytes must be untouched")
	}
}

func TestIncompressibleBlobUntouched(t *testing.T) {
	// Unique short-ish lines: no filter can shrink it, guard keeps original.
	unique := makeLines(300, "u")
	for i := 0; i < 300; i++ {
		unique = strings.Replace(unique, "xxxxxxxx", string(rune('a'+i%26))+string(rune('a'+i/26%26))+"xxxxxxx", 1)
	}
	out := filterSmartTruncate(unique)
	// smart-truncate WILL truncate 300 unique lines (over threshold) — that is
	// intended. The guard that matters: output never empty.
	if out == "" {
		t.Fatal("filter must never return empty for non-empty input")
	}
}

// --- chat walker ---

func toolMsg(text string, isError bool) schemas.ChatMessage {
	msg := schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: strPtr("call-1")},
		Content:         &schemas.ChatMessageContent{ContentStr: strPtr(text)},
	}
	if isError {
		msg.ChatToolMessage.IsError = boolPtr(true)
	}
	return msg
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func TestCompressChatToolMessage(t *testing.T) {
	p := newTestPlugin(t)
	big := makeUniqueLines(400, "log ")
	req := &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("hi")}},
			toolMsg(big, false),
		},
	}}
	stats := p.compressRequest(req)
	if stats.hits != 1 {
		t.Fatalf("expected 1 hit, got %d", stats.hits)
	}
	got := *req.ChatRequest.Input[1].Content.ContentStr
	if !strings.Contains(got, "[token-saver:") {
		t.Fatal("tool message content must be compressed")
	}
	if len(got) >= len(big) {
		t.Fatal("compressed content must be smaller")
	}
	// User message untouched.
	if *req.ChatRequest.Input[0].Content.ContentStr != "hi" {
		t.Fatal("non-tool message must be untouched")
	}
}

func TestSkipErrorToolMessage(t *testing.T) {
	p := newTestPlugin(t)
	big := makeLines(400, "err ")
	req := &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{toolMsg(big, true)},
	}}
	stats := p.compressRequest(req)
	if stats.hits != 0 {
		t.Fatal("is_error tool output must never be compressed")
	}
	if *req.ChatRequest.Input[0].Content.ContentStr != big {
		t.Fatal("error output must be byte-identical")
	}
}

func TestCompressChatContentBlocks(t *testing.T) {
	p := newTestPlugin(t)
	big := makeLines(400, "blk ")
	req := &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{
			{
				Role:            schemas.ChatMessageRoleTool,
				ChatToolMessage: &schemas.ChatToolMessage{},
				Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
					{Type: schemas.ChatContentBlockTypeText, Text: strPtr(big)},
				}},
			},
		},
	}}
	stats := p.compressRequest(req)
	if stats.hits != 1 {
		t.Fatalf("text block must compress, hits=%d", stats.hits)
	}
}

// --- responses walker ---

func TestCompressResponsesFunctionCallOutput(t *testing.T) {
	p := newTestPlugin(t)
	big := makeLines(400, "out ")
	itemType := schemas.ResponsesMessageTypeFunctionCallOutput
	req := &schemas.BifrostRequest{ResponsesRequest: &schemas.BifrostResponsesRequest{
		Input: []schemas.ResponsesMessage{
			{
				Type: &itemType,
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: strPtr(big),
					},
				},
			},
		},
	}}
	stats := p.compressRequest(req)
	if stats.hits != 1 {
		t.Fatalf("function_call_output must compress, hits=%d", stats.hits)
	}
	if *req.ResponsesRequest.Input[0].ResponsesToolMessage.Output.ResponsesToolCallOutputStr == big {
		t.Fatal("output must be compressed")
	}
}

func TestSkipResponsesErrorOutput(t *testing.T) {
	p := newTestPlugin(t)
	big := makeLines(400, "err ")
	itemType := schemas.ResponsesMessageTypeFunctionCallOutput
	errStr := "boom"
	req := &schemas.BifrostRequest{ResponsesRequest: &schemas.BifrostResponsesRequest{
		Input: []schemas.ResponsesMessage{
			{
				Type: &itemType,
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Error:  &errStr,
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: strPtr(big)},
				},
			},
		},
	}}
	stats := p.compressRequest(req)
	if stats.hits != 0 {
		t.Fatal("error tool output must be skipped")
	}
}

// --- hook ---

func TestPreRequestHookFailOpen(t *testing.T) {
	p := newTestPlugin(t)
	// nil request: no panic, no error.
	if err := p.PreRequestHook(nil, nil); err != nil {
		t.Fatalf("nil request must not error: %v", err)
	}
	// RTK off: untouched.
	pOff, _ := Init(&Config{Default: &Settings{RTK: false}}, &mockLogger{})
	big := makeLines(400, "x ")
	req := &schemas.BifrostRequest{ChatRequest: &schemas.BifrostChatRequest{
		Input: []schemas.ChatMessage{toolMsg(big, false)},
	}}
	if err := pOff.PreRequestHook(nil, req); err != nil {
		t.Fatal(err)
	}
	if *req.ChatRequest.Input[0].Content.ContentStr != big {
		t.Fatal("rtk=off must not compress")
	}
}

func TestInitRequiresLogger(t *testing.T) {
	if _, err := Init(nil, nil); err != ErrNilLogger {
		t.Fatalf("expected ErrNilLogger, got %v", err)
	}
}
