package service

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
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

func TestBuildAPIRequestLogTrainingItemsChatMessagesDoNotDuplicate(t *testing.T) {
	requestBody := `{"messages":[{"role":"user","content":"hello chat"}]}`
	items, status, parseError := BuildAPIRequestLogTrainingItems(requestBody, "")
	if status != model.APIRequestLogParseOK {
		t.Fatalf("expected ok parse status, got %s: %s", status, parseError)
	}

	count := 0
	for _, item := range items {
		if item.Phase == model.APIRequestLogPhaseInput &&
			item.ItemType == model.APIRequestLogItemMessage &&
			item.Role == "user" &&
			strings.Contains(string(item.Content), "hello chat") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one chat user item, got %d: %+v", count, items)
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

func TestBuildAPIRequestLogTrainingItemsPreservesDeveloperRole(t *testing.T) {
	requestBody := `{"input":[{"type":"message","role":"developer","content":"developer instruction"}]}`
	items, status, parseError := BuildAPIRequestLogTrainingItems(requestBody, "")
	if status != model.APIRequestLogParseOK {
		t.Fatalf("expected ok parse status, got %s: %s", status, parseError)
	}
	assertHasItem(t, items, model.APIRequestLogPhaseInput, model.APIRequestLogItemMessage, "developer", "developer instruction")
}

func TestAPIRequestLogSSECollectorKeepsOnlyCompletedResponsesItem(t *testing.T) {
	collector := newAPIRequestLogSSECollector(false)
	collector.Feed([]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"in_progress\",\"phase\":\"final_answer\",\"content\":[]}}\n\n"))
	collector.Feed([]byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"hel\"}\n\n"))
	collector.Feed([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"phase\":\"final_answer\",\"role\":\"assistant\",\"metadata\":{\"turn_id\":\"turn_1\"},\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n"))
	collector.Feed([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"completed\",\"phase\":\"final_answer\",\"role\":\"assistant\",\"metadata\":{\"turn_id\":\"turn_1\"},\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}}\n\n"))
	collector.Feed([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\n"))

	snapshot := collector.Snapshot()
	if !snapshot.completed || snapshot.completionSignal != "responses.message.final.completed" {
		t.Fatalf("expected exact final completion, got completed=%v signal=%q", snapshot.completed, snapshot.completionSignal)
	}
	if len(snapshot.items) != 1 {
		t.Fatalf("expected one completed item, got %d: %+v", len(snapshot.items), snapshot.items)
	}
	if string(snapshot.items[0].Content) != "hello" {
		t.Fatalf("unexpected completed content: %q", snapshot.items[0].Content)
	}
	if len(snapshot.itemMeta) != 1 {
		t.Fatalf("expected one item metadata row, got %d", len(snapshot.itemMeta))
	}
	meta := snapshot.itemMeta[0]
	if meta.ProviderItemID != "msg_1" || meta.TurnID != "turn_1" || meta.MessagePhase != "final_answer" || meta.Status != "completed" {
		t.Fatalf("unexpected item metadata: %+v", meta)
	}
}

func TestAPIRequestLogSSECollectorUsesResponseCompletedOutputAsFallback(t *testing.T) {
	collector := newAPIRequestLogSSECollector(false)
	collector.Feed([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":\"msg_fallback\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"fallback\"}]}]}}\n\n"))
	snapshot := collector.Snapshot()
	if snapshot.completed {
		t.Fatalf("response.completed fallback must not complete the full agent turn: %+v", snapshot)
	}
	if len(snapshot.items) != 1 || string(snapshot.items[0].Content) != "fallback" {
		t.Fatalf("expected missing done item to be recovered from response.completed, got %+v", snapshot.items)
	}
	if len(snapshot.itemMeta) != 1 || snapshot.itemMeta[0].ProviderItemID != "msg_fallback" {
		t.Fatalf("unexpected fallback metadata: %+v", snapshot.itemMeta)
	}
}

func TestAPIRequestLogSSECollectorBoundsOversizedEvent(t *testing.T) {
	oldMaxItemBytes := common.APIRequestLogMaxItemBytes
	common.APIRequestLogMaxItemBytes = 512
	t.Cleanup(func() { common.APIRequestLogMaxItemBytes = oldMaxItemBytes })

	collector := newAPIRequestLogSSECollector(false)
	collector.Feed([]byte("data: " + strings.Repeat("x", 2048)))
	if len(collector.pending) > common.APIRequestLogMaxItemBytes {
		t.Fatalf("pending SSE line exceeded configured bound: %d", len(collector.pending))
	}
	collector.Feed([]byte("\n\n"))
	collector.Feed([]byte("data: " + strings.Repeat("y", 2048) + "\n\n"))
	collector.Feed([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_small\",\"type\":\"message\",\"status\":\"completed\",\"phase\":\"final\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\n"))
	snapshot := collector.Snapshot()
	if len(snapshot.parseErrors) != 1 || !strings.Contains(snapshot.parseErrors[0], "exceeds 512 bytes") {
		t.Fatalf("expected one bounded-event error, got %+v", snapshot.parseErrors)
	}
	if !snapshot.completed || len(snapshot.items) != 1 || string(snapshot.items[0].Content) != "ok" {
		t.Fatalf("collector did not recover after oversized event: %+v", snapshot)
	}
}

func TestAPIRequestLogSSECollectorDoesNotCompleteOnResponseCompleted(t *testing.T) {
	collector := newAPIRequestLogSSECollector(false)
	collector.Feed([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_commentary\",\"type\":\"message\",\"status\":\"completed\",\"phase\":\"commentary\",\"content\":[{\"type\":\"output_text\",\"text\":\"working\"}]}}\n\n"))
	collector.Feed([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	snapshot := collector.Snapshot()
	if snapshot.completed {
		t.Fatalf("response.completed and commentary must not complete an agent turn: %+v", snapshot)
	}
	if len(snapshot.items) != 1 || string(snapshot.items[0].Content) != "working" {
		t.Fatalf("expected commentary item to remain visible, got %+v", snapshot.items)
	}
}

func TestAPIRequestLogSSECollectorProtocolTerminalSignals(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		signal     string
		wantAnswer string
	}{
		{
			name: "chat",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"},\"finish_reason\":null}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n",
			signal:     "chat.finish_reason:stop",
			wantAnswer: "hello",
		},
		{
			name: "claude",
			body: "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n",
			signal:     "claude.message_stop",
			wantAnswer: "hello",
		},
		{
			name:       "gemini",
			body:       "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}\n\n",
			signal:     "gemini.finish_reason:STOP",
			wantAnswer: "hello",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := newAPIRequestLogSSECollector(false)
			collector.Feed([]byte(tt.body))
			snapshot := collector.Snapshot()
			if !snapshot.completed || snapshot.completionSignal != tt.signal {
				t.Fatalf("unexpected completion: completed=%v signal=%q", snapshot.completed, snapshot.completionSignal)
			}
			if len(snapshot.items) != 1 || string(snapshot.items[0].Content) != tt.wantAnswer {
				t.Fatalf("unexpected normalized output: %+v", snapshot.items)
			}
		})
	}
}

func TestAPIRequestLogSSECollectorBoundsGenericDeltaAggregatesAndStillCompletes(t *testing.T) {
	oldMaxItemBytes := common.APIRequestLogMaxItemBytes
	common.APIRequestLogMaxItemBytes = 257
	t.Cleanup(func() { common.APIRequestLogMaxItemBytes = oldMaxItemBytes })

	collector := newAPIRequestLogSSECollector(false)
	delta := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"界\",\"reasoning_content\":\"思\"}}]}\n\n")
	for idx := 0; idx < 200; idx++ {
		collector.Feed(delta)
	}
	if collector.answer.Len() > collector.aggregateLimit || collector.reasoning.Len() > collector.aggregateLimit {
		t.Fatalf("generic accumulators exceeded limit: answer=%d reasoning=%d limit=%d", collector.answer.Len(), collector.reasoning.Len(), collector.aggregateLimit)
	}

	collector.Feed([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
	snapshot := collector.Snapshot()
	if !snapshot.completed || snapshot.completionSignal != "chat.finish_reason:stop" {
		t.Fatalf("terminal event was lost after aggregate truncation: %+v", snapshot)
	}
	if len(snapshot.parseErrors) != 1 || !strings.Contains(snapshot.parseErrors[0], "generic output exceeds 257 bytes") {
		t.Fatalf("expected one aggregate warning, got %+v", snapshot.parseErrors)
	}

	found := map[string]bool{}
	for _, item := range snapshot.items {
		if item.ItemType != model.APIRequestLogItemMessage && item.ItemType != model.APIRequestLogItemReasoning {
			continue
		}
		content := string(item.Content)
		if len(content) > collector.aggregateLimit {
			t.Fatalf("persisted %s content exceeded limit: %d", item.ItemType, len(content))
		}
		if !utf8.ValidString(content) {
			t.Fatalf("persisted %s content ended on an invalid UTF-8 boundary: %q", item.ItemType, content)
		}
		if !item.Truncated {
			t.Fatalf("persisted %s item was not marked truncated: %+v", item.ItemType, item)
		}
		found[item.ItemType] = true
	}
	if !found[model.APIRequestLogItemMessage] || !found[model.APIRequestLogItemReasoning] {
		t.Fatalf("answer and reasoning must have independent bounded items: %+v", snapshot.items)
	}
}

func TestAPIRequestLogSSECollectorBoundsAndDeduplicatesFallbacksWithDonePriority(t *testing.T) {
	oldMaxItemBytes := common.APIRequestLogMaxItemBytes
	common.APIRequestLogMaxItemBytes = 512
	t.Cleanup(func() { common.APIRequestLogMaxItemBytes = oldMaxItemBytes })

	collector := newAPIRequestLogSSECollector(false)
	fallbackEvent := func(id, text string) []byte {
		return []byte(fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"id\":%q,\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", id, text))
	}
	for idx := 0; idx < 100; idx++ {
		collector.Feed(fallbackEvent("fallback-0", "duplicate"))
	}
	if len(collector.fallbackItems) != 1 || collector.fallbackWarning {
		t.Fatalf("duplicate fallbacks consumed queue budget: items=%d bytes=%d warning=%v", len(collector.fallbackItems), collector.fallbackBytes, collector.fallbackWarning)
	}
	for idx := 1; idx < 50; idx++ {
		collector.Feed(fallbackEvent(fmt.Sprintf("fallback-%d", idx), "candidate"))
	}
	if collector.fallbackBytes > collector.aggregateLimit {
		t.Fatalf("fallback queue exceeded byte budget: %d > %d", collector.fallbackBytes, collector.aggregateLimit)
	}
	if len(collector.fallbackItems) >= 50 {
		t.Fatalf("fallback queue was not bounded: %d items", len(collector.fallbackItems))
	}
	if len(collector.parseErrors) != 1 || !strings.Contains(collector.parseErrors[0], "fallback exceeds 512 cumulative bytes") {
		t.Fatalf("expected one fallback budget warning, got %+v", collector.parseErrors)
	}

	collector.Feed([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fallback-0\",\"type\":\"message\",\"status\":\"completed\",\"phase\":\"final\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"authoritative\"}]}}\n\n"))
	snapshot := collector.Snapshot()
	if !snapshot.completed || snapshot.completionSignal != "responses.message.final.completed" {
		t.Fatalf("done event did not retain final completion priority: %+v", snapshot)
	}
	doneCount := 0
	for idx, meta := range snapshot.itemMeta {
		if meta.ProviderItemID != "fallback-0" {
			continue
		}
		doneCount++
		if string(snapshot.items[idx].Content) != "authoritative" || snapshot.items[idx].Source != "sse.output_item.done" {
			t.Fatalf("fallback replaced authoritative done item: %+v", snapshot.items[idx])
		}
	}
	if doneCount != 1 {
		t.Fatalf("expected one authoritative done item, got %d: %+v", doneCount, snapshot.items)
	}
}

func TestAPIRequestLogSSECollectorUsesFourMiBDefaultAggregateLimit(t *testing.T) {
	oldMaxItemBytes := common.APIRequestLogMaxItemBytes
	common.APIRequestLogMaxItemBytes = 0
	t.Cleanup(func() { common.APIRequestLogMaxItemBytes = oldMaxItemBytes })

	collector := newAPIRequestLogSSECollector(false)
	if collector.aggregateLimit != 4*1024*1024 {
		t.Fatalf("unexpected default aggregate limit: %d", collector.aggregateLimit)
	}
}

func TestAPIRequestLogNonStreamingClaudeStopReasonCompletesRequestTurn(t *testing.T) {
	builder, status, parseError := buildAPIRequestLogTrainingItems("", `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}`, nil)
	if status != model.APIRequestLogParseOK || parseError != "" {
		t.Fatalf("unexpected parse result: %s %s", status, parseError)
	}
	if !builder.completed || builder.completionSignal != "claude.stop_reason:end_turn" {
		t.Fatalf("unexpected completion: completed=%v signal=%q", builder.completed, builder.completionSignal)
	}
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
