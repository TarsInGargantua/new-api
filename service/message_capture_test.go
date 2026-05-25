package service

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
)

func TestConversationIDFromOpenAIRequestMetadata(t *testing.T) {
	req := &dto.GeneralOpenAIRequest{
		Metadata: []byte(`{"conversation_id":"conv-123"}`),
	}
	if got := conversationIDFromOpenAIRequest(req); got != "conv-123" {
		t.Fatalf("conversationIDFromOpenAIRequest = %q", got)
	}
}

func TestConversationIDFromResponsesConversation(t *testing.T) {
	req := &dto.OpenAIResponsesRequest{
		Conversation: []byte(`{"id":"resp-conv"}`),
	}
	if got := conversationIDFromResponsesRequest(req); got != "resp-conv" {
		t.Fatalf("conversationIDFromResponsesRequest = %q", got)
	}
}

func TestConversationIDFromMetadataSessionID(t *testing.T) {
	if got := conversationIDFromRawJSON([]byte(`{"session_id":"sess-123"}`)); got != "sess-123" {
		t.Fatalf("conversationIDFromRawJSON = %q", got)
	}
}

func TestExtractJSONResponseSummaryOpenAIChat(t *testing.T) {
	body := `{"choices":[{"message":{"content":"hello","reasoning_content":"because"}}]}`
	answer, reasoning := extractJSONResponseSummary(body)
	if answer != "hello" {
		t.Fatalf("answer = %q, want hello", answer)
	}
	if reasoning != "because" {
		t.Fatalf("reasoning = %q, want because", reasoning)
	}
}

func TestExtractSSESummaryOpenAIChat(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"r1\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n" +
		"data: [DONE]\n\n"
	answer, reasoning := extractResponseSummary(body)
	if answer != "hello" {
		t.Fatalf("answer = %q, want hello", answer)
	}
	if reasoning != "r1" {
		t.Fatalf("reasoning = %q, want r1", reasoning)
	}
}

func TestExtractJSONResponseSummaryResponsesAPI(t *testing.T) {
	body := `{"output":[{"type":"message","content":[{"type":"output_text","text":"done"}]},{"type":"reasoning","content":[{"type":"summary_text","text":"planned"}]}]}`
	answer, reasoning := extractJSONResponseSummary(body)
	if answer != "done" {
		t.Fatalf("answer = %q, want done", answer)
	}
	if reasoning != "planned" {
		t.Fatalf("reasoning = %q, want planned", reasoning)
	}
}

func TestExtractJSONResponseSummaryClaudeAndGemini(t *testing.T) {
	claude := `{"content":[{"type":"thinking","thinking":"think"},{"type":"text","text":"answer"}]}`
	answer, reasoning := extractJSONResponseSummary(claude)
	if answer != "answer" || reasoning != "think" {
		t.Fatalf("claude answer=%q reasoning=%q", answer, reasoning)
	}

	gemini := `{"candidates":[{"content":{"parts":[{"thought":true,"text":"think"},{"text":"answer"}]}}]}`
	answer, reasoning = extractJSONResponseSummary(gemini)
	if answer != "answer" || reasoning != "think" {
		t.Fatalf("gemini answer=%q reasoning=%q", answer, reasoning)
	}
}

func TestExtractJSONResponseSummaryIgnoresObjectDelta(t *testing.T) {
	body := `{"type":"response.output_text.delta","delta":{"text":"ignored"}}`
	answer, reasoning := extractJSONResponseSummary(body)
	if answer != "" {
		t.Fatalf("answer = %q, want empty", answer)
	}
	if reasoning != "" {
		t.Fatalf("reasoning = %q, want empty", reasoning)
	}
}

func TestResponsesInputToText(t *testing.T) {
	input := []byte(`[{"role":"user","content":[{"type":"input_text","text":"hello"},{"type":"input_image","image_url":"data:"}]}]`)
	if got := responsesInputToText(input); got != "hello\n[input_image]" {
		t.Fatalf("responsesInputToText = %q", got)
	}
}
