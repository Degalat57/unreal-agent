package operation

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
)

const (
	RemoteJobResultFilename = "result"
	RemoteJobErrorFilename  = "error"
)

// prepareRemoteJobOutputDirectory defaults the artifact output directory of a
// remote job operation so handlers that round-trip its state capture
// untruncated output; an invalid or already-configured state is left unchanged.
func prepareRemoteJobOutputDirectory(current *Operation, directory string) {
	if directory == "" {
		return
	}
	state, err := DecodeRemoteJobState(*current)
	if err != nil || state.OutputDirectory != "" {
		return
	}
	state.OutputDirectory = directory
	encoded, err := json.Marshal(state)
	if err != nil {
		return
	}
	current.State = encoded
}

func prepareRemoteJobOutput(current Operation, state *RemoteJobState) {
	limit := current.MaxOutputLength
	if !state.ResultTruncated || state.TerminalResult == "" {
		state.ResultBytes = len(state.TerminalResult)
		state.TerminalResult, state.ResultTruncated = boundRemoteJobOutput(
			current.ID,
			state.TerminalResult,
			limit,
			state.OutputDirectory,
			RemoteJobResultFilename,
			&state.ResultPath,
		)
	}
	if !state.ErrorTruncated || state.TerminalError == "" {
		state.ErrorBytes = len(state.TerminalError)
		state.TerminalError, state.ErrorTruncated = boundRemoteJobOutput(
			current.ID,
			state.TerminalError,
			limit,
			state.OutputDirectory,
			RemoteJobErrorFilename,
			&state.ErrorPath,
		)
	}
}

// boundRemoteJobOutput bounds output like BoundOutput; when the output is
// truncated and directory is set, the full output is first captured in an
// artifact file whose path is recorded and referenced in the bounded output.
func boundRemoteJobOutput(
	id ID,
	text string,
	limit int,
	directory string,
	name string,
	path *string,
) (string, bool) {
	bounded, truncated := BoundOutput(text, limit)
	if !truncated {
		return bounded, false
	}
	if captured := captureRemoteJobOutput(id, directory, name, text); captured != "" {
		*path = captured
		bounded, truncated = boundOutput(text, "", int64(len(text)), limit, captured)
	}
	return bounded, truncated
}

func captureRemoteJobOutput(id ID, directory, name, text string) string {
	if directory == "" || text == "" {
		return ""
	}
	path, err := remoteJobArtifactPath(directory, id, name)
	if err != nil {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ""
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return ""
	}
	return path
}

func remoteJobArtifactPath(directory string, id ID, name string) (string, error) {
	component := string(id)
	if component == "" || component == "." || component == ".." ||
		filepath.IsAbs(component) || filepath.Base(component) != component {
		return "", fmt.Errorf("operation ID %q is not a path component", id)
	}
	return filepath.Join(directory, component, name), nil
}
