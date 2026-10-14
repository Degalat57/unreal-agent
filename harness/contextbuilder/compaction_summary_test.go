package contextbuilder

import (
	"encoding/json/jsontext"
	"reflect"
	"testing"

	"github.com/unreallabsai/unreal-agent/harness/llm"
)

func TestCompactionKeepsContextWithoutUsableSummary(t *testing.T) {
	for _, test := range []struct {
		name   string
		output []llm.Item
	}{
		{name: "empty output"},
		{name: "empty message", output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: ""}}}},
		{name: "whitespace message", output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: " \n\t"}}}},
		{name: "user message only", output: []llm.Item{{Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleUser, Text: "Summary"}}}},
		{name: "reasoning only", output: []llm.Item{{Type: llm.ItemProvider, Data: llm.ProviderItem{Type: "reasoning", Raw: jsontext.Value(`{"type":"reasoning"}`), Display: &llm.ProviderDisplay{Kind: llm.ProviderDisplayReasoning, Text: "thinking"}}}}},
		{name: "tool call only", output: []llm.Item{{Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "call", Name: "test", Arguments: `{}`}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := newCompactionHistory(t)
			before := buildCompactionContext(t, current)
			rebuilt, ok := current.Compact(llm.Response{Stop: llm.StopComplete, Output: test.output})
			if ok || rebuilt != current {
				t.Fatal("unusable summary replaced the builder")
			}
			if after := buildCompactionContext(t, current); !reflect.DeepEqual(after, before) {
				t.Fatal("rejected compaction changed the context")
			}
			if _, ok := current.compactionCutoff(); !ok {
				t.Fatal("rejected compaction lost the eligible prefix")
			}
			rebuilt, ok = current.Compact(usableCompactionResponse("Summary"))
			if !ok || rebuilt == current {
				t.Fatal("usable summary after rejection did not compact")
			}
		})
	}
}
