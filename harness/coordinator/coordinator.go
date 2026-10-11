// Package coordinator defines the owner of the central event loop.
package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/contextbuilder"
	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/operation"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/tool"
)

// TurnFailedError reports that the provider failed a regular turn's model
// response outright. The failed response is durably recorded in the session
// store before the error is returned, its partial output never runs as tool
// calls, and resuming the session does not rerun the failed request; a newly
// submitted input starts a fresh turn.
type TurnFailedError struct {
	TurnID  session.TurnID
	Failure llm.Failure
}

func (err *TurnFailedError) Error() string {
	if err.Failure.Code == "" {
		return fmt.Sprintf("model response for turn %q failed: %s", err.TurnID, err.Failure.Message)
	}
	return fmt.Sprintf("model response for turn %q failed: %s: %s", err.TurnID, err.Failure.Code, err.Failure.Message)
}

type Dependencies struct {
	ToolHeartbeatInterval time.Duration
	SessionID             session.ID
	Inbox                 *inbox.Inbox
	Restored              sessionstore.ResumeState
	Sessions              sessionstore.Store
	ContextBuilder        contextbuilder.Builder
	LLM                   llm.Adapter
	Tools                 tool.Registry
	Operations            operation.Manager
}

type Coordinator interface {
	// Run owns one session's decision loop until a stop control completes or
	// the context is canceled. It returns nil for a completed stop. It is
	// single-use; its caller must cancel the Inbox when Run returns.
	Run(context.Context) error
}

func New(dependencies Dependencies) Coordinator {
	return &coordinator{
		dependencies: dependencies,
		state:        newLoopState(),
	}
}
