package coordinator

import (
	"encoding/json/v2"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
)

// unusableCompactionResponses are completed responses without a usable summary.
var unusableCompactionResponses = []llm.Response{
	{Stop: llm.StopComplete},
	{Stop: llm.StopComplete, Output: textResponse(" \n\t").Output},
	{Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call", Name: "unknown", Arguments: `{}`}}}},
	{Stop: llm.StopComplete, Output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "not an assistant summary"}}}},
}

func TestAutomaticCompactionDisabledAfterRepeatedUnusableSummaries(t *testing.T) {
	for _, test := range []struct {
		name     string
		attempts int
		want     int
	}{
		{name: "default", want: DefaultMaxCompactionAttempts},
		{name: "configured", attempts: 1, want: 1},
		{name: "above default", attempts: 4, want: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				run := newCompactionRun(t)
				run.current.dependencies.MaxCompactionAttempts = test.attempts
				run.start(t)
				before := run.current.dependencies.ContextBuilder
				run.input(t, externalEvent(t, 0, "pending", "Continue"))
				for attempt := range test.want {
					assertCompactionTurn(t, run, attempt+1, session.TurnCompaction)
					run.respond(t, attempt, unusableCompactionResponses[attempt%len(unusableCompactionResponses)])
					if before != run.current.dependencies.ContextBuilder || run.current.pendingInputs() != 1 {
						t.Fatal("unusable summary replaced context or delivered input")
					}
					if len(compactionNotices(t, run)) != 0 && attempt+1 < test.want {
						t.Fatalf("notice posted after %d of %d attempts", attempt+1, test.want)
					}
				}
				if !run.current.state.autoCompactionOff || run.current.state.compactionFailures != test.want {
					t.Fatalf("bookkeeping after %d failures = %+v", test.want, run.current.state)
				}
				// The pending input continues on an ordinary turn instead of another compaction.
				assertCompactionTurn(t, run, test.want+1, session.TurnRegular)
				if !requestHasMessage(run.calls[test.want].request, "Continue") {
					t.Fatal("ordinary turn after disabled compaction dropped the pending input")
				}
				notices := compactionNotices(t, run)
				if len(notices) != 1 {
					t.Fatalf("notices = %d, want one", len(notices))
				}
				for _, want := range []string{"Compaction failed", "Automatic compaction is now off", `"compact"`} {
					if !strings.Contains(notices[0].Reason, want) {
						t.Fatalf("notice %q lacks %q", notices[0].Reason, want)
					}
				}
				pressured := textResponse("Working")
				pressured.Usage = llm.Usage{InputTokens: 40_000, OutputTokens: 1_000}
				run.respond(t, test.want, pressured)
				// Context pressure persists, yet no automatic compaction starts.
				run.input(t, externalEvent(t, 0, "more", "More work"))
				if !run.current.dependencies.ContextBuilder.NeedsCompaction() {
					t.Fatal("context pressure vanished")
				}
				assertCompactionTurn(t, run, test.want+2, session.TurnRegular)
				run.respond(t, test.want+1, pressured)
				if len(compactionNotices(t, run)) != 1 {
					t.Fatal("ordinary turns repeated the notice")
				}
				// Manual compaction still works and re-enables automatic compaction.
				run.input(t, compactInput(t, "manual"))
				assertCompactionTurn(t, run, test.want+3, session.TurnCompaction)
				if len(run.calls[test.want+2].request.Tools) != 0 {
					t.Fatal("manual compaction exposed tools")
				}
				run.input(t, stopInput(t, "idle", inbox.StopWhenIdle))
				run.respond(t, test.want+2, llm.Response{Stop: llm.StopComplete, Output: textResponse("Summary").Output})
				if run.current.dependencies.ContextBuilder == before || run.current.state.autoCompactionOff || run.current.state.compactionFailures != 0 {
					t.Fatalf("manual summary did not restore compaction: %+v", run.current.state)
				}
				run.assertStopped(t)
				if len(run.calls) != test.want+3 {
					t.Fatalf("calls = %d, want %d", len(run.calls), test.want+3)
				}
			})
		})
	}
}

func TestManualCompactionFailureRepeatsNotice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := newCompactionRun(t)
		run.current.dependencies.MaxCompactionAttempts = 1
		run.start(t)
		before := run.current.dependencies.ContextBuilder
		run.input(t, externalEvent(t, 0, "pending", "Continue"))
		assertCompactionTurn(t, run, 1, session.TurnCompaction)
		run.respond(t, 0, unusableCompactionResponses[0])
		assertCompactionTurn(t, run, 2, session.TurnRegular)
		pressured := textResponse("Working")
		pressured.Usage = llm.Usage{InputTokens: 40_000, OutputTokens: 1_000}
		run.respond(t, 1, pressured)
		run.input(t, compactInput(t, "manual"))
		assertCompactionTurn(t, run, 3, session.TurnCompaction)
		run.respond(t, 2, unusableCompactionResponses[1])
		if before != run.current.dependencies.ContextBuilder || !run.current.state.autoCompactionOff || run.current.state.compactionFailures != 2 {
			t.Fatalf("failed manual compaction changed context or bookkeeping: %+v", run.current.state)
		}
		run.assertRunning(t)
		if len(run.calls) != 3 {
			t.Fatal("failed manual compaction started another request")
		}
		notices := compactionNotices(t, run)
		if len(notices) != 2 || !strings.Contains(notices[1].Reason, "2 consecutive") {
			t.Fatalf("notices = %+v, want a second one counting both failures", notices)
		}
		run.input(t, stopInput(t, "idle", inbox.StopWhenIdle))
		run.assertStopped(t)
	})
}

func TestManualCompactionWithoutHistoryDoesNotStartTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := newStopTestRun(t, 0)
		run.start(t)
		run.input(t, compactInput(t, "manual"))
		if len(run.calls) != 0 || !run.current.state.compactionRequested {
			t.Fatalf("calls = %d, requested = %t", len(run.calls), run.current.state.compactionRequested)
		}
		run.input(t, externalEvent(t, 0, "pending", "hello"))
		if len(run.calls) != 1 || run.current.state.currentTurnType != session.TurnRegular || run.current.state.compactionRequested {
			t.Fatalf("calls = %d, turn = %s, requested = %t", len(run.calls), run.current.state.currentTurnType, run.current.state.compactionRequested)
		}
		run.input(t, stopInput(t, "idle", inbox.StopWhenIdle))
		run.respond(t, 0, textResponse("Done"))
		run.assertStopped(t)
	})
}

func TestCompactionFailureBookkeepingMatchesReplay(t *testing.T) {
	current, directory := recordedCompactionHistory(t)
	appendCompactionTestItem(t, current, sessionstore.Item{Kind: sessionstore.ItemInput, Data: externalEvent(t, 0, "pending", "Continue")})
	before, err := current.dependencies.ContextBuilder.Build()
	if err != nil {
		t.Fatal(err)
	}
	previous := session.TurnID("B")
	for attempt := range DefaultMaxCompactionAttempts {
		turn := session.Turn{ID: session.TurnID(string(rune('a' + attempt))), PreviousTurnID: previous, Type: session.TurnCompaction}
		appendCompactionTestItem(t, current, sessionstore.Item{Kind: sessionstore.ItemTurn, Data: turn})
		response := sessionstore.ModelResponse{TurnID: turn.ID, Response: unusableCompactionResponses[attempt%len(unusableCompactionResponses)]}
		if _, err := current.handleModelResponse(t.Context(), response); err != nil {
			t.Fatal(err)
		}
		previous = turn.ID
	}
	live, err := current.dependencies.ContextBuilder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, before) || !current.state.autoCompactionOff {
		t.Fatalf("unusable summaries changed context or left compaction enabled: %+v", current.state)
	}
	if current.shouldCompact() {
		t.Fatal("disabled compaction still scheduled")
	}
	replayed := replayCompactionHistory(t, directory)
	if !reflect.DeepEqual(replayed.state, current.state) || replayed.shouldCompact() {
		t.Fatalf("replayed state = %+v, want %+v", replayed.state, current.state)
	}
	assertSameContext(t, current, replayed)

	// A manual request followed by a usable summary re-enables compaction on both.
	for _, item := range []sessionstore.Item{
		{Kind: sessionstore.ItemInput, Data: compactInput(t, "manual")},
		{Kind: sessionstore.ItemTurn, Data: session.Turn{ID: "manual", PreviousTurnID: previous, Type: session.TurnCompaction}},
		{Kind: sessionstore.ItemModelResponse, Data: sessionstore.ModelResponse{TurnID: "manual", Response: llm.Response{Stop: llm.StopComplete, Output: textResponse("Summary").Output}}},
	} {
		appendCompactionTestItem(t, current, item)
	}
	if current.state.autoCompactionOff || current.state.compactionFailures != 0 || current.state.compactionRequested {
		t.Fatalf("usable summary did not reset bookkeeping: %+v", current.state)
	}
	replayed = replayCompactionHistory(t, directory)
	if !reflect.DeepEqual(replayed.state, current.state) {
		t.Fatalf("replayed state = %+v, want %+v", replayed.state, current.state)
	}
	assertSameContext(t, current, replayed)
}

func TestCompactionNoticeReplaysWithoutModelMessage(t *testing.T) {
	current, directory := recordedCompactionHistory(t)
	before, err := current.dependencies.ContextBuilder.Build()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(inbox.ControlMessage{Mode: inbox.CompactionDisabled, Reason: "Compaction failed"})
	if err != nil {
		t.Fatal(err)
	}
	notice := inbox.Input{ID: "notice", Kind: inbox.InputControl, Payload: payload}
	if err := current.handleInboxInput(t.Context(), notice); err != nil {
		t.Fatal(err)
	}
	live, err := current.dependencies.ContextBuilder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, before) || current.pendingInputs() != 0 || current.state.callModel {
		t.Fatalf("notice changed the model context or scheduled a turn: %+v", current.state)
	}
	replayed := replayCompactionHistory(t, directory)
	if !reflect.DeepEqual(replayed.state, current.state) {
		t.Fatalf("replayed state = %+v, want %+v", replayed.state, current.state)
	}
	assertSameContext(t, current, replayed)
}

func TestCoordinatorRejectsNegativeCompactionAttempts(t *testing.T) {
	run := newStopTestRun(t, 0)
	run.current.dependencies.MaxCompactionAttempts = -1
	err := run.current.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "max compaction attempts") {
		t.Fatalf("Run error = %v", err)
	}
}

func requestHasMessage(request llm.Request, text string) bool {
	for _, item := range request.Input {
		if item.Type == llm.ItemMessage && item.Data.(llm.Message).Text == text {
			return true
		}
	}
	return false
}

func compactInput(t *testing.T, id inbox.ID) inbox.Input {
	t.Helper()
	payload, err := json.Marshal(inbox.ControlMessage{Mode: inbox.Compact})
	if err != nil {
		t.Fatal(err)
	}
	return inbox.Input{ID: id, Kind: inbox.InputControl, Payload: payload}
}

// compactionNotices returns the persisted CompactionDisabled controls in order.
func compactionNotices(t *testing.T, run *stopTestRun) []inbox.ControlMessage {
	t.Helper()
	page, err := run.current.dependencies.Sessions.Items(t.Context(), "session-1", sessionstore.BeforeFirst, 100)
	if err != nil {
		t.Fatal(err)
	}
	var notices []inbox.ControlMessage
	for _, item := range page.Items {
		input, ok := item.Data.(inbox.Input)
		if !ok || input.Kind != inbox.InputControl {
			continue
		}
		request, err := input.DecodeControlMessage()
		if err != nil {
			t.Fatal(err)
		}
		if request.Mode == inbox.CompactionDisabled {
			notices = append(notices, request)
		}
	}
	return notices
}

func replayCompactionHistory(t *testing.T, directory string) *coordinator {
	t.Helper()
	reopened, err := localfile.New(directory)
	if err != nil {
		t.Fatal(err)
	}
	replayed := newStopTestRun(t, 0).current
	replayed.dependencies.Sessions = reopened
	if err := replayed.loadHistory(t.Context()); err != nil {
		t.Fatal(err)
	}
	return replayed
}

func assertSameContext(t *testing.T, live, replayed *coordinator) {
	t.Helper()
	built, err := live.dependencies.ContextBuilder.Build()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := replayed.dependencies.ContextBuilder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(built, restored) {
		t.Fatal("live and replayed context differ")
	}
}
