package operation_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/operation"
)

func TestRemoteJobCapturesFullResultBeforeTruncation(t *testing.T) {
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: "test", Version: 1, Data: jsontext.Value(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	current := operation.Operation{ID: "remote", Type: spec.Type, Version: spec.Version, Status: operation.StatusReady, State: spec.State, MaxOutputLength: 3}
	state, err := operation.DecodeRemoteJobState(current)
	if err != nil {
		t.Fatal(err)
	}
	state.OutputDirectory = directory
	state.TerminalResult = `"éééé"`
	step, err := operation.UpdateRemoteJob(current, state, operation.StatusCompleted)
	if err != nil {
		t.Fatal(err)
	}
	var prepared operation.RemoteJobState
	if err := json.Unmarshal(step.Operation.State, &prepared); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(directory, "remote", operation.RemoteJobResultFilename)
	if prepared.ResultPath != wantPath {
		t.Fatalf("result path = %q, want %q", prepared.ResultPath, wantPath)
	}
	if !strings.HasPrefix(prepared.TerminalResult, `"...`) ||
		!strings.Contains(prepared.TerminalResult, "bytes truncated; complete output in "+wantPath) ||
		!strings.HasSuffix(prepared.TerminalResult, `...é"`) {
		t.Fatalf("prepared result = %q", prepared.TerminalResult)
	}
	captured, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(captured) != `"éééé"` {
		t.Fatalf("captured result = %q", captured)
	}
	info, err := os.Stat(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("captured result mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestRemoteJobCapturesFullErrorBeforeTruncation(t *testing.T) {
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: "test", Version: 1, Data: jsontext.Value(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	current := operation.Operation{ID: "remote", Type: spec.Type, Version: spec.Version, Status: operation.StatusReady, State: spec.State, MaxOutputLength: 3}
	state, err := operation.DecodeRemoteJobState(current)
	if err != nil {
		t.Fatal(err)
	}
	state.OutputDirectory = directory
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	current.State = encoded
	failed, err := operation.FailRemoteJob(current, errors.New("éééé"))
	if err != nil {
		t.Fatal(err)
	}
	var prepared operation.RemoteJobState
	if err := json.Unmarshal(failed.Operation.State, &prepared); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(directory, "remote", operation.RemoteJobErrorFilename)
	if prepared.ErrorPath != wantPath {
		t.Fatalf("error path = %q, want %q", prepared.ErrorPath, wantPath)
	}
	if !strings.Contains(prepared.TerminalError, "bytes truncated; complete output in "+wantPath) {
		t.Fatalf("prepared error = %q", prepared.TerminalError)
	}
	captured, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(captured) != "éééé" {
		t.Fatalf("captured error = %q", captured)
	}
}

func TestRemoteJobSkipsArtifactCaptureForSmallOutput(t *testing.T) {
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: "test", Version: 1, Data: jsontext.Value(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	current := operation.Operation{ID: "remote", Type: spec.Type, Version: spec.Version, Status: operation.StatusReady, State: spec.State, MaxOutputLength: operation.DefaultMaxOutputLength}
	state, err := operation.DecodeRemoteJobState(current)
	if err != nil {
		t.Fatal(err)
	}
	state.OutputDirectory = directory
	state.TerminalResult = `{"content":[]}`
	step, err := operation.UpdateRemoteJob(current, state, operation.StatusCompleted)
	if err != nil {
		t.Fatal(err)
	}
	var prepared operation.RemoteJobState
	if err := json.Unmarshal(step.Operation.State, &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.TerminalResult != `{"content":[]}` || prepared.ResultTruncated || prepared.ResultPath != "" {
		t.Fatalf("prepared result = %#v", prepared)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("artifact directory holds %d entries, want none", len(entries))
	}
}

func TestRemoteJobSkipsArtifactCaptureForEmptyOutput(t *testing.T) {
	spec, err := operation.NewRemoteJobSpec(operation.RemoteJobPlan{Type: "test", Version: 1, Data: jsontext.Value(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	current := operation.Operation{ID: "remote", Type: spec.Type, Version: spec.Version, Status: operation.StatusReady, State: spec.State, MaxOutputLength: 3}
	state, err := operation.DecodeRemoteJobState(current)
	if err != nil {
		t.Fatal(err)
	}
	state.OutputDirectory = directory
	step, err := operation.UpdateRemoteJob(current, state, operation.StatusCanceled)
	if err != nil {
		t.Fatal(err)
	}
	var prepared operation.RemoteJobState
	if err := json.Unmarshal(step.Operation.State, &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.ResultPath != "" || prepared.ErrorPath != "" {
		t.Fatalf("prepared state = %#v", prepared)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("artifact directory holds %d entries, want none", len(entries))
	}
}
