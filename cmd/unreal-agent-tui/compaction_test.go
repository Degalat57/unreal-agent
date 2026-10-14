package main

import (
	"encoding/json/v2"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
)

func TestComposeInputCompactCommand(t *testing.T) {
	for _, test := range []struct {
		text string
		mode inbox.ControlMode
	}{
		{text: "/compact", mode: inbox.Compact},
		{text: "  /compact\n", mode: inbox.Compact},
		{text: "/compact now"},
		{text: "hello"},
	} {
		t.Run(test.text, func(t *testing.T) {
			input, err := composeInput(test.text)
			if err != nil {
				t.Fatal(err)
			}
			if input.ID == "" || input.Validate() != nil {
				t.Fatalf("input = %+v", input)
			}
			if test.mode == "" {
				var text string
				if input.Kind != inbox.InputExternal || json.Unmarshal(input.Payload, &text) != nil || text != test.text {
					t.Fatalf("input = %+v, want external %q", input, test.text)
				}
				return
			}
			request, err := input.DecodeControlMessage()
			if input.Kind != inbox.InputControl || err != nil || request.Mode != test.mode {
				t.Fatalf("input = %+v, error = %v, want %s control", input, err, test.mode)
			}
		})
	}
}

func TestModelShowsCompactionNotice(t *testing.T) {
	theme, err := loadTheme(defaultTheme)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), nil, nil, t.TempDir(), t.TempDir(), options{theme: theme})
	for _, test := range []struct {
		name    string
		message inbox.ControlMessage
		label   string
	}{
		{name: "notice", message: inbox.ControlMessage{Mode: inbox.CompactionDisabled, Reason: "Compaction failed: automatic compaction is now off."}, label: "Compaction failed"},
		{name: "heartbeat", message: inbox.ControlMessage{Mode: inbox.Heartbeat, Reason: "waiting"}},
		{name: "compact", message: inbox.ControlMessage{Mode: inbox.Compact}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(test.message)
			if err != nil {
				t.Fatal(err)
			}
			before := len(m.lines)
			m.apply(sessionstore.Item{Kind: sessionstore.ItemInput, Data: inbox.Input{ID: inbox.ID(test.name), Kind: inbox.InputControl, Payload: payload}})
			if test.label == "" {
				if len(m.lines) != before {
					t.Fatalf("control %s added lines %+v", test.message.Mode, m.lines[before:])
				}
				return
			}
			if len(m.lines) != before+1 || m.lines[before].label != test.label || m.lines[before].text != test.message.Reason {
				t.Fatalf("lines = %+v, want %q notice", m.lines[before:], test.label)
			}
		})
	}
}
