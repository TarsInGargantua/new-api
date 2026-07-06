package service

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
)

func TestBuildAPIRequestLogTrainingItemsResponsesInputDoesNotDuplicate(t *testing.T) {
	requestBody := `{"input":[{"role":"user","content":"hello responses"}]}`
	items, status, parseError := BuildAPIRequestLogTrainingItems(requestBody, "")
	if status != model.APIRequestLogParseOK {
		t.Fatalf("expected ok parse status, got %s: %s", status, parseError)
	}

	count := 0
	for _, item := range items {
		if item.Phase == model.APIRequestLogPhaseInput &&
			item.ItemType == model.APIRequestLogItemMessage &&
			item.Role == "user" &&
			strings.Contains(string(item.Content), "hello responses") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one user input item, got %d: %+v", count, items)
	}
}

func TestBuildAPIRequestLogTrainingItemsCapturesReasoningToolAndEncrypted(t *testing.T) {
	requestBody := `{
		"messages":[
			{"role":"system","content":"system prompt"},
			{"role":"user","content":"call tool"},
			{"role":"tool","tool_call_id":"call_1","content":"tool output"}
		],
		"tools":[{"type":"function","function":{"name":"lookup"}}]
	}`
	responseBody := `{
		"output":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"reasoning summary"}],"encrypted_content":"encrypted-reasoning"},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"},
			{"type":"message","content":[{"type":"output_text","text":"assistant output"}]}
		]
	}`

	items, status, parseError := BuildAPIRequestLogTrainingItems(requestBody, responseBody)
	if status != model.APIRequestLogParseOK {
		t.Fatalf("expected ok parse status, got %s: %s", status, parseError)
	}

	assertHasItem(t, items, model.APIRequestLogPhaseInput, model.APIRequestLogItemMessage, "system", "system prompt")
	assertHasItem(t, items, model.APIRequestLogPhaseInput, model.APIRequestLogItemToolSpec, "", "lookup")
	assertHasItem(t, items, model.APIRequestLogPhaseInput, model.APIRequestLogItemToolResult, "tool", "tool output")
	assertHasItem(t, items, model.APIRequestLogPhaseOutput, model.APIRequestLogItemReasoning, "assistant", "reasoning summary")
	assertHasItem(t, items, model.APIRequestLogPhaseOutput, model.APIRequestLogItemReasoning, "assistant", "encrypted-reasoning")
	assertHasItem(t, items, model.APIRequestLogPhaseOutput, model.APIRequestLogItemToolCall, "assistant", "lookup")
	assertHasItem(t, items, model.APIRequestLogPhaseOutput, model.APIRequestLogItemMessage, "assistant", "assistant output")
}

func assertHasItem(t *testing.T, items []model.APIRequestLogItem, phase string, itemType string, role string, content string) {
	t.Helper()
	for _, item := range items {
		if item.Phase == phase &&
			item.ItemType == itemType &&
			item.Role == role &&
			strings.Contains(string(item.Content), content) {
			return
		}
	}
	t.Fatalf("missing item phase=%s type=%s role=%s content=%q in %+v", phase, itemType, role, content, items)
}
