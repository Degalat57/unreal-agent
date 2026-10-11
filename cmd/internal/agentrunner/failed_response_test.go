package agentrunner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/unreallabsai/unreal-agent/cmd/internal/providers"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/llm/clients/openai"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

func quotaExhaustedResponse() llm.Response {
	return llm.Response{
		Failure: &llm.Failure{Code: "insufficient_quota", Message: "You exceeded your current quota."},
		Output: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "partial"}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-1", Name: "Bash", Arguments: `{"command":"printf hi"}`}},
		},
		Usage: llm.Usage{TokenUsage: llm.TokenUsage{InputTokens: 5, OutputTokens: 2}},
	}
}

// A provider response that reports a failure without a stop classification
// ends the process with a nonzero exit, keeps the failure and usage in the
// durable session store, and never dispatches the response's partial tool
// calls. Resuming the session surfaces the recorded failure instead of
// rerunning the failed request, and a newly submitted input runs a fresh turn.
func TestRunMainFailedResponseExitsNonzero(t *testing.T) {
	workspace, sessions := t.TempDir(), t.TempDir()
	const request = `{"session_id":"failed-turn","messages":[{"role":"user","content":"run it","message_id":"69621f8d-4f4d-49a5-8f7d-3b24fd855c01"}]}`
	var mu sync.Mutex
	calls := 0
	client := &fakeClient{respond: func(context.Context, llm.Request) (llm.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return quotaExhaustedResponse(), nil
	}}
	ctx := context.Background()
	run := func(request string, active *fakeClient) (int, string, string) {
		var stdout, stderr strings.Builder
		code := RunMain(ctx, []string{"-workspace", workspace, "-session-directory", sessions},
			func(name string) string {
				switch name {
				case providers.APIKeyEnvironment:
					return "secret"
				case "SHELL":
					return "/bin/sh"
				default:
					return ""
				}
			}, func() []string { return []string{"PATH=/usr/bin:/bin"} },
			strings.NewReader(request), &stdout, &stderr, testConfig(active))
		return code, stdout.String(), stderr.String()
	}
	code, _, stderr := run(request, client)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr = %q)", code, stderr)
	}
	if !strings.Contains(stderr, "insufficient_quota") || !strings.Contains(stderr, "You exceeded your current quota.") {
		t.Fatalf("stderr = %q, want the surfaced provider failure", stderr)
	}
	store, err := localfile.New(sessions)
	if err != nil {
		t.Fatal(err)
	}
	items := readLogHistory(t, store, "failed-turn", 100)
	var failed bool
	statuses := 0
	for _, item := range items {
		switch response := item.Data.(type) {
		case sessionstore.ModelResponse:
			if response.Response.Failure == nil || response.Response.Failure.Code != "insufficient_quota" {
				t.Fatalf("stored response = %#v, want the recorded failure", response.Response)
			}
			if response.Response.Usage.InputTokens != 5 || response.Response.Usage.OutputTokens != 2 {
				t.Fatalf("stored usage = %#v, want the recorded usage", response.Response.Usage)
			}
			failed = true
		case sessionstore.ToolCallStatus:
			statuses++
		}
	}
	if !failed || statuses != 0 {
		t.Fatalf("durable outcome: failure recorded = %v, tool call statuses = %d", failed, statuses)
	}
	operations, err := filepath.Glob(filepath.Join(sessions, "operations", "*", "*", "*"))
	if err != nil || len(operations) != 0 {
		t.Fatalf("failed response dispatched operations: %v, %v", operations, err)
	}

	// Resuming with the same message surfaces the recorded failure again
	// without rerunning the request.
	code, _, stderr = run(request, client)
	mu.Lock()
	total := calls
	mu.Unlock()
	if code != 1 || total != 1 {
		t.Fatalf("resume: exit = %d, model calls = %d, want 1 exit and 1 call (stderr = %q)", code, total, stderr)
	}

	// A newly submitted input runs a fresh turn that completes normally.
	const retry = `{"session_id":"failed-turn","messages":[{"role":"user","content":"try again","message_id":"69621f8d-4f4d-49a5-8f7d-3b24fd855c02"}]}`
	recovered := &fakeClient{respond: func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Stop: llm.StopComplete, Output: []llm.Item{{
			Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "Done."},
		}}}, nil
	}}
	code, _, stderr = run(retry, recovered)
	if code != 0 {
		t.Fatalf("fresh turn: exit = %d, want 0 (stderr = %q)", code, stderr)
	}
}

// End to end over the wire: an HTTP 200 SSE stream whose terminal event is
// response.failed (insufficient_quota) with partial output, including a
// completed function call, exits nonzero and records the failure durably,
// while an ordinary response.completed stream keeps exiting zero.
func TestRunMainProviderSSEFailedResponse(t *testing.T) {
	const failedStream = `data: {"type":"response.failed","response":{"id":"resp-1","status":"failed","error":{"code":"insufficient_quota","message":"You exceeded your current quota."},"usage":{"input_tokens":5,"input_tokens_details":{},"output_tokens":2,"output_tokens_details":{}},"output":[{"type":"message","id":"msg-1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"partial answer"}]},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"Bash","arguments":"{\"command\":\"printf hi\"}","status":"completed"}]}}

`
	const completedStream = `data: {"type":"response.completed","response":{"id":"resp-2","status":"completed","usage":{"input_tokens":5,"input_tokens_details":{},"output_tokens":2,"output_tokens_details":{}},"output":[{"type":"message","id":"msg-2","status":"completed","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}}

`
	for _, test := range []struct {
		name       string
		stream     string
		wantExit   int
		wantFailed bool
	}{
		{name: "response.failed", stream: failedStream, wantExit: 1, wantFailed: true},
		{name: "response.completed", stream: completedStream, wantExit: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := fmt.Fprint(w, test.stream); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			workspace, sessions := t.TempDir(), t.TempDir()
			const request = `{"session_id":"failed-sse","messages":[{"role":"user","content":"run it","message_id":"69621f8d-4f4d-49a5-8f7d-3b24fd855c01"}]}`
			var stdout, stderr strings.Builder
			code := RunMain(context.Background(), []string{"-workspace", workspace, "-session-directory", sessions},
				func(name string) string {
					switch name {
					case providers.APIKeyEnvironment:
						return "secret"
					case "SHELL":
						return "/bin/sh"
					default:
						return ""
					}
				}, func() []string { return []string{"PATH=/usr/bin:/bin"} },
				strings.NewReader(request), &stdout, &stderr, sseRunnerConfig(t, server.URL))
			if code != test.wantExit {
				t.Fatalf("exit = %d, want %d (stdout = %q, stderr = %q)", code, test.wantExit, stdout.String(), stderr.String())
			}
			store, err := localfile.New(sessions)
			if err != nil {
				t.Fatal(err)
			}
			failed, statuses := false, 0
			for _, item := range readLogHistory(t, store, "failed-sse", 100) {
				switch value := item.Data.(type) {
				case sessionstore.ModelResponse:
					failed = value.Response.Failure != nil && value.Response.Failure.Code == "insufficient_quota"
				case sessionstore.ToolCallStatus:
					statuses++
				}
			}
			if failed != test.wantFailed {
				t.Fatalf("durable failure recorded = %v, want %v", failed, test.wantFailed)
			}
			if test.wantFailed && statuses != 0 {
				t.Fatalf("failed stream dispatched %d partial tool calls", statuses)
			}
			if test.wantFailed && !strings.Contains(stderr.String(), "insufficient_quota") {
				t.Fatalf("stderr = %q, want the surfaced provider failure", stderr.String())
			}
		})
	}
}

func sseRunnerConfig(t *testing.T, baseURL string) Config {
	t.Helper()
	return Config{Name: "unreal-agent-runner", ParseRequest: parseTestRequest, Providers: []providers.Provider{{
		Name: "openai", BaseURL: baseURL,
		DefaultModel:      "gpt-default",
		APIKeyEnvironment: "OPENAI_API_KEY",
		NewClient: func(apiKey, endpoint string, maxAttempts int, _ func(string) string) (providers.Client, error) {
			return openai.NewClient(openai.Config{APIKey: apiKey, BaseURL: endpoint, MaxAttempts: &maxAttempts})
		},
	}}}
}
