package coordinator

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

func failedResponse() llm.Response {
	return llm.Response{
		Failure: &llm.Failure{Code: "insufficient_quota", Message: "You exceeded your current quota."},
		Output: []llm.Item{
			{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "partial"}},
			{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call-1", Name: "Bash", Arguments: `{"command":"printf hi"}`}},
		},
		Usage: llm.Usage{TokenUsage: llm.TokenUsage{InputTokens: 7, OutputTokens: 3}},
	}
}

func assertTurnFailedError(t *testing.T, err error, turnID session.TurnID) {
	t.Helper()
	var failed *TurnFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Run error = %v, want TurnFailedError", err)
	}
	if failed.TurnID != turnID || failed.Failure.Code != "insufficient_quota" || failed.Failure.Message == "" {
		t.Fatalf("TurnFailedError = %#v, want turn %q with the recorded failure", failed, turnID)
	}
}

// A provider-reported failure ends the turn: the failure and usage stay in
// the durable session store, partial tool calls are never dispatched, and Run
// returns a typed error instead of exiting successfully.
func TestCoordinatorFailedResponseFailsTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := newStopTestRun(t, 0)
		run.start(t)
		run.input(t, externalEvent(t, 0, "pending", "Run the task"))
		if len(run.calls) != 1 {
			t.Fatalf("requests = %d, want 1", len(run.calls))
		}
		response := failedResponse()
		run.respond(t, 0, response)
		turnID := session.TurnID(run.store.appendedTurns[0].ID)
		select {
		case err := <-run.done:
			assertTurnFailedError(t, err, turnID)
		default:
			t.Fatal("Run did not stop on the failed response")
		}
		if len(run.store.appendedResponses) != 1 || run.store.appendedResponses[0].TurnID != turnID {
			t.Fatalf("appended responses = %#v, want the failed response for turn %q", run.store.appendedResponses, turnID)
		}
		if stored := run.store.appendedResponses[0].Response; !reflect.DeepEqual(stored, response) {
			t.Fatalf("stored response = %#v, want %#v", stored, response)
		}
		if len(run.store.appendedStatuses) != 0 || len(run.operations.adds) != 0 {
			t.Fatalf("failed response dispatched partial tool calls: statuses = %#v, operations = %#v",
				run.store.appendedStatuses, run.operations.adds)
		}
	})
}

// Ordinary completed responses keep exiting successfully.
func TestCoordinatorCompletedResponseStillSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		run := newStopTestRun(t, 0)
		run.start(t)
		run.input(t, externalEvent(t, 0, "pending", "Run the task"))
		run.input(t, stopInput(t, "stop", inbox.StopWhenIdle))
		run.respond(t, 0, textResponse("Done."))
		run.assertStopped(t)
	})
}

// Replaying a session whose latest regular turn failed surfaces the recorded
// failure instead of exiting successfully, without rerunning the failed
// request or resurrecting its partial tool calls.
func TestCoordinatorFailedResponseReplaySurfacesErrorWithoutRerun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := emptyFakeStore()
		store.items = []sessionstore.Item{
			storedItem(1, sessionstore.ItemTurn, session.Turn{ID: "turn-1", Type: session.TurnRegular}),
			storedItem(2, sessionstore.ItemModelResponse, sessionstore.ModelResponse{TurnID: "turn-1", Response: failedResponse()}),
		}
		adapter := &fakeAdapter{}
		operations := newFakeOperationManager()
		current := newTestCoordinatorWithAdapter(store, newTestInbox(t), operations, contextbuilder.NewBuilder(), tool.NewRegistry(tool.StaticTranslators{}), adapter)
		done := make(chan error, 1)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		go func() { done <- current.Run(ctx) }()
		synctest.Wait()
		synctest.Sleep(2 * slurpIdleTimeout)
		submitTestInput(t, current.dependencies.Inbox, stopInput(t, "stop", inbox.StopWhenIdle))
		synctest.Wait()
		assertTurnFailedError(t, <-done, "turn-1")
		if requests := adapter.requestSnapshot(); len(requests) != 0 {
			t.Fatalf("replayed failure reran the request: %#v", requests)
		}
		if len(operations.adds) != 0 || len(store.appendedStatuses) != 0 {
			t.Fatalf("replayed failure dispatched partial tool calls: operations = %#v, statuses = %#v",
				operations.adds, store.appendedStatuses)
		}
	})
}

// A newly submitted input after a failed turn starts a fresh turn instead of
// rerunning the failed request; the fresh turn can complete normally.
func TestCoordinatorNewInputAfterFailedTurnRunsFreshTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := emptyFakeStore()
		store.items = []sessionstore.Item{
			storedItem(1, sessionstore.ItemTurn, session.Turn{ID: "turn-1", Type: session.TurnRegular}),
			storedItem(2, sessionstore.ItemModelResponse, sessionstore.ModelResponse{TurnID: "turn-1", Response: failedResponse()}),
		}
		adapter := &fakeAdapter{respond: func(_ context.Context, _ llm.Request) (llm.Response, error) {
			return textResponse("Recovered."), nil
		}}
		operations := newFakeOperationManager()
		current := newTestCoordinatorWithAdapter(store, newTestInbox(t), operations, contextbuilder.NewBuilder(), tool.NewRegistry(tool.StaticTranslators{}), adapter)
		done := make(chan error, 1)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		go func() { done <- current.Run(ctx) }()
		synctest.Wait()
		synctest.Sleep(2 * slurpIdleTimeout)
		submitTestInput(t, current.dependencies.Inbox, externalEvent(t, 0, "retry", "Try again"))
		synctest.Wait()
		synctest.Sleep(2 * slurpIdleTimeout)
		submitTestInput(t, current.dependencies.Inbox, stopInput(t, "stop", inbox.StopWhenIdle))
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatalf("Run error = %v, want fresh turn to complete", err)
		}
		requests := adapter.requestSnapshot()
		if len(requests) != 1 {
			t.Fatalf("requests = %d, want exactly one fresh turn", len(requests))
		}
		if request := requests[0]; len(request.Input) == 0 || request.Input[len(request.Input)-1].Type != llm.ItemMessage {
			t.Fatalf("fresh request = %#v, want the newly submitted input", request)
		}
	})
}
