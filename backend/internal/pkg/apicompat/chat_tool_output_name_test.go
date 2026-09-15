package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatToolOutputPreservesNameWithoutCallID(t *testing.T) {
	for _, tc := range []struct{ name, callID, toolName, wantName string }{
		{"named standalone output", "", "get_status", "get_status"},
		{"linked output unchanged", "call_123", "get_status", ""},
		{"missing identity not invented", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := &ChatCompletionsRequest{Model: "gpt-5.6-sol", Messages: []ChatMessage{{Role: "tool", ToolCallID: tc.callID, Name: tc.toolName, Content: json.RawMessage(`"ready"`)}}}
			response, err := ChatCompletionsToResponses(request)
			require.NoError(t, err)
			var items []ResponsesInputItem
			require.NoError(t, json.Unmarshal(response.Input, &items))
			require.Len(t, items, 1)
			require.Equal(t, "function_call_output", items[0].Type)
			require.Equal(t, tc.callID, items[0].CallID)
			require.Equal(t, tc.wantName, items[0].Name)
			require.Equal(t, "ready", items[0].Output)
		})
	}
}
