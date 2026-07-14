package service

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

const messageCaptureKey = "message_capture"

const (
	captureRoleUser      = "user"
	captureRoleAssistant = "assistant"
	captureRoleSystem    = "system"
	captureRoleTool      = "tool"
)

type messageCapture struct {
	ConversationID string                 `json:"conversation_id,omitempty"`
	Question       string                 `json:"question,omitempty"`
	ModelReasoning string                 `json:"model_reasoning,omitempty"`
	Answer         string                 `json:"answer,omitempty"`
	Messages       []capturedMessage      `json:"messages,omitempty"`
	RawRequest     *capturedBody          `json:"raw_request,omitempty"`
	RawResponse    *capturedBody          `json:"raw_response,omitempty"`
	Meta           map[string]interface{} `json:"meta,omitempty"`
}

type capturedMessage struct {
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	Source    string `json:"source,omitempty"`
}

type capturedBody struct {
	ContentType   string `json:"content_type,omitempty"`
	Body          string `json:"body,omitempty"`
	OmittedReason string `json:"omitted_reason,omitempty"`
	Size          int64  `json:"size,omitempty"`
	CapturedBytes int    `json:"captured_bytes,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	Redacted      bool   `json:"redacted,omitempty"`
	Status        int    `json:"status,omitempty"`
}

func appendMessageCapture(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other map[string]interface{}) {
	if other == nil || !common.AuditContentEnabled {
		return
	}
	capture := buildMessageCapture(ctx, relayInfo, other)
	if capture == nil || capture.isEmpty() {
		return
	}
	other[messageCaptureKey] = capture.toMap()
}

func buildMessageCapture(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other map[string]interface{}) *messageCapture {
	capture := &messageCapture{
		Meta: make(map[string]interface{}),
	}

	if relayInfo != nil {
		capture.ConversationID = conversationIDFromRelay(ctx, relayInfo)
		capture.Meta["relay_format"] = string(relayInfo.RelayFormat)
		capture.Meta["final_request_format"] = string(relayInfo.GetFinalRequestRelayFormat())
		capture.Meta["model"] = relayInfo.OriginModelName
		capture.Meta["is_stream"] = relayInfo.IsStream
		capture.Meta["request_path"] = relayInfo.RequestURLPath
	}

	if audit, ok := other["audit_content"].(map[string]interface{}); ok {
		capture.RawRequest = capturedBodyFromAudit(auditMap(audit, "request"))
		capture.RawResponse = capturedBodyFromAudit(auditMap(audit, "response"))
	}
	if summary, ok := contentAuditResponseSummaryFromContext(ctx); ok {
		capture.Answer = firstNonEmpty(capture.Answer, summary.Answer)
		capture.ModelReasoning = firstNonEmpty(capture.ModelReasoning, summary.Reasoning)
	} else if capture.RawResponse != nil && capture.RawResponse.Body != "" {
		answer, reasoning := extractResponseSummary(capture.RawResponse.Body)
		capture.Answer = firstNonEmpty(capture.Answer, answer)
		capture.ModelReasoning = firstNonEmpty(capture.ModelReasoning, reasoning)
	}

	if len(capture.Meta) == 0 {
		capture.Meta = nil
	}
	return capture
}

func contentAuditResponseSummaryFromContext(ctx *gin.Context) (contentAuditResponseSummary, bool) {
	if ctx == nil {
		return contentAuditResponseSummary{}, false
	}
	raw, exists := ctx.Get(contentAuditResponseSummaryKey)
	if !exists {
		return contentAuditResponseSummary{}, false
	}
	summary, ok := raw.(contentAuditResponseSummary)
	return summary, ok
}

func (c *messageCapture) isEmpty() bool {
	if c == nil {
		return true
	}
	return c.ConversationID == "" && c.Question == "" && c.ModelReasoning == "" && c.Answer == "" && len(c.Messages) == 0 && c.RawRequest == nil && c.RawResponse == nil
}

func (c *messageCapture) toMap() map[string]interface{} {
	if c == nil {
		return nil
	}
	out := make(map[string]interface{})
	if c.ConversationID != "" {
		out["conversation_id"] = c.ConversationID
	}
	if c.ModelReasoning != "" {
		out["model_reasoning"] = c.ModelReasoning
	}
	if c.Answer != "" {
		out["answer"] = c.Answer
	}
	if c.RawRequest != nil {
		out["raw_request"] = c.RawRequest.toMap()
	}
	if c.RawResponse != nil {
		out["raw_response"] = c.RawResponse.toMap()
	}
	if len(c.Meta) > 0 {
		out["meta"] = c.Meta
	}
	return out
}

func (b *capturedBody) toMap() map[string]interface{} {
	if b == nil {
		return nil
	}
	out := make(map[string]interface{})
	if b.ContentType != "" {
		out["content_type"] = b.ContentType
	}
	if b.Body != "" {
		out["body"] = b.Body
	}
	if b.OmittedReason != "" {
		out["omitted_reason"] = b.OmittedReason
	}
	if b.Size > 0 {
		out["size"] = b.Size
	}
	if b.CapturedBytes > 0 {
		out["captured_bytes"] = b.CapturedBytes
	}
	if b.Truncated {
		out["truncated"] = true
	}
	if b.Redacted {
		out["redacted"] = true
	}
	if b.Status > 0 {
		out["status"] = b.Status
	}
	return out
}

func capturedMessagesFromRelayRequest(info *relaycommon.RelayInfo) []capturedMessage {
	if info == nil || info.Request == nil {
		return nil
	}

	switch req := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		return capturedMessagesFromOpenAIRequest(req)
	case *dto.OpenAIResponsesRequest:
		return capturedMessagesFromResponsesRequest(req)
	case *dto.ClaudeRequest:
		return capturedMessagesFromClaudeRequest(req)
	case *dto.GeminiChatRequest:
		return capturedMessagesFromGeminiRequest(req)
	default:
		return nil
	}
}

func conversationIDFromRelay(ctx *gin.Context, info *relaycommon.RelayInfo) string {
	if id := conversationIDFromHeaders(ctx); id != "" {
		return id
	}
	if info == nil || info.Request == nil {
		return ""
	}
	switch req := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		return conversationIDFromOpenAIRequest(req)
	case *dto.OpenAIResponsesRequest:
		return conversationIDFromResponsesRequest(req)
	case *dto.ClaudeRequest:
		return conversationIDFromClaudeRequest(req)
	case *dto.GeminiChatRequest:
		return conversationIDFromGeminiRequest(req)
	default:
		return ""
	}
}

func conversationIDFromHeaders(ctx *gin.Context) string {
	if ctx == nil || ctx.Request == nil {
		return ""
	}
	for _, key := range []string{
		"X-Conversation-Id",
		"X-Conversation-ID",
		"Conversation-Id",
		"Conversation-ID",
		"X-Session-Id",
		"X-Session-ID",
		"Session-Id",
		"Session-ID",
		"session_id",
	} {
		if id := strings.TrimSpace(ctx.Request.Header.Get(key)); id != "" {
			return id
		}
	}
	return ""
}

func conversationIDFromOpenAIRequest(req *dto.GeneralOpenAIRequest) string {
	if req == nil {
		return ""
	}
	if id := conversationIDFromRawJSON(req.Metadata); id != "" {
		return id
	}
	if id := strings.TrimSpace(req.PromptCacheKey); id != "" {
		return id
	}
	return ""
}

func conversationIDFromResponsesRequest(req *dto.OpenAIResponsesRequest) string {
	if req == nil {
		return ""
	}
	for _, raw := range [][]byte{
		req.Metadata,
		req.PromptCacheKey,
	} {
		if id := conversationIDFromRawJSON(raw); id != "" {
			return id
		}
	}
	if id := conversationIDFromConversationJSON(req.Conversation); id != "" {
		return id
	}
	return ""
}

func conversationIDFromClaudeRequest(req *dto.ClaudeRequest) string {
	if req == nil {
		return ""
	}
	if id := conversationIDFromRawJSON(req.Metadata); id != "" {
		return id
	}
	return ""
}

func conversationIDFromGeminiRequest(req *dto.GeminiChatRequest) string {
	if req == nil {
		return ""
	}
	return strings.TrimSpace(req.CachedContent)
}

func conversationIDFromRawJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if err := common.Unmarshal(raw, &str); err == nil {
		return strings.TrimSpace(str)
	}
	var value interface{}
	if err := common.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return conversationIDFromValue(value)
}

func conversationIDFromConversationJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if id := conversationIDFromRawJSON(raw); id != "" {
		return id
	}
	var obj map[string]interface{}
	if err := common.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	return conversationIDFromValue(obj["id"])
}

func conversationIDFromValue(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]interface{}:
		for _, key := range []string{
			"conversation_id",
			"conversationId",
			"conversation",
			"session_id",
			"sessionId",
			"thread_id",
			"threadId",
			"chat_id",
			"chatId",
		} {
			if id := conversationIDFromValue(v[key]); id != "" {
				return id
			}
		}
	case []interface{}:
		for _, item := range v {
			if id := conversationIDFromValue(item); id != "" {
				return id
			}
		}
	}
	return ""
}

func capturedMessagesFromOpenAIRequest(req *dto.GeneralOpenAIRequest) []capturedMessage {
	if req == nil {
		return nil
	}
	messages := make([]capturedMessage, 0, len(req.Messages)+2)
	if req.Instruction != "" {
		messages = append(messages, capturedMessage{Role: captureRoleSystem, Content: req.Instruction, Source: "instruction"})
	}
	if prompt := anyToText(req.Prompt); prompt != "" {
		messages = append(messages, capturedMessage{Role: captureRoleUser, Content: prompt, Source: "prompt"})
	}
	if input := anyToText(req.Input); input != "" {
		messages = append(messages, capturedMessage{Role: captureRoleUser, Content: input, Source: "input"})
	}
	for _, msg := range req.Messages {
		content := openAIMessageContentToText(msg)
		reasoning := msg.GetReasoningContent()
		if content == "" && reasoning == "" {
			continue
		}
		role := normalizeCaptureRole(msg.Role)
		messages = append(messages, capturedMessage{Role: role, Content: content, Reasoning: reasoning, Source: "messages"})
	}
	return messages
}

func capturedMessagesFromResponsesRequest(req *dto.OpenAIResponsesRequest) []capturedMessage {
	if req == nil {
		return nil
	}
	messages := make([]capturedMessage, 0, 3)
	if instructions := rawMessageToText(req.Instructions); instructions != "" {
		messages = append(messages, capturedMessage{Role: captureRoleSystem, Content: instructions, Source: "instructions"})
	}
	if input := responsesInputToText(req.Input); input != "" {
		messages = append(messages, capturedMessage{Role: captureRoleUser, Content: input, Source: "input"})
	}
	if prompt := rawMessageToText(req.Prompt); prompt != "" {
		messages = append(messages, capturedMessage{Role: captureRoleUser, Content: prompt, Source: "prompt"})
	}
	return messages
}

func capturedMessagesFromClaudeRequest(req *dto.ClaudeRequest) []capturedMessage {
	if req == nil {
		return nil
	}
	messages := make([]capturedMessage, 0, len(req.Messages)+1)
	if system := anyToText(req.System); system != "" {
		messages = append(messages, capturedMessage{Role: captureRoleSystem, Content: system, Source: "system"})
	}
	if req.Prompt != "" {
		messages = append(messages, capturedMessage{Role: captureRoleUser, Content: req.Prompt, Source: "prompt"})
	}
	for _, msg := range req.Messages {
		content := msg.GetStringContent()
		if content == "" {
			content = anyToText(msg.Content)
		}
		if content == "" {
			continue
		}
		messages = append(messages, capturedMessage{Role: normalizeCaptureRole(msg.Role), Content: content, Source: "messages"})
	}
	return messages
}

func capturedMessagesFromGeminiRequest(req *dto.GeminiChatRequest) []capturedMessage {
	if req == nil {
		return nil
	}
	messages := make([]capturedMessage, 0, len(req.Contents)+1)
	if req.SystemInstructions != nil {
		if system := geminiContentToText(*req.SystemInstructions); system != "" {
			messages = append(messages, capturedMessage{Role: captureRoleSystem, Content: system, Source: "systemInstruction"})
		}
	}
	for _, content := range req.Contents {
		text := geminiContentToText(content)
		if text == "" {
			continue
		}
		messages = append(messages, capturedMessage{Role: normalizeCaptureRole(content.Role), Content: text, Source: "contents"})
	}
	return messages
}

func openAIMessageContentToText(msg dto.Message) string {
	if msg.Content == nil {
		return ""
	}
	text := msg.StringContent()
	if text != "" {
		return text
	}
	return anyToText(msg.Content)
}

func responsesInputToText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if err := common.Unmarshal(raw, &str); err == nil {
		return strings.TrimSpace(str)
	}
	var inputs []dto.Input
	if err := common.Unmarshal(raw, &inputs); err == nil {
		parts := make([]string, 0, len(inputs))
		for _, input := range inputs {
			if input.Role != "" && len(input.Content) > 0 {
				if content := rawMessageToText(input.Content); content != "" {
					parts = append(parts, content)
				}
				continue
			}
			if input.Type != "" && len(input.Content) > 0 {
				if content := rawMessageToText(input.Content); content != "" {
					parts = append(parts, content)
				}
			}
		}
		if len(parts) > 0 {
			return strings.TrimSpace(strings.Join(parts, "\n"))
		}
	}
	var generic []interface{}
	if err := common.Unmarshal(raw, &generic); err == nil {
		return anyToText(generic)
	}
	return rawMessageToText(raw)
}

func geminiContentToText(content dto.GeminiChatContent) string {
	parts := make([]string, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part.Text != "" {
			parts = append(parts, part.Text)
		}
		if part.FunctionCall != nil {
			parts = append(parts, fmt.Sprintf("[function_call:%s]", part.FunctionCall.FunctionName))
		}
		if part.FunctionResponse != nil {
			parts = append(parts, fmt.Sprintf("[function_response:%s]", part.FunctionResponse.Name))
		}
		if part.FileData != nil && part.FileData.FileUri != "" {
			parts = append(parts, "[file]")
		}
		if part.InlineData != nil {
			parts = append(parts, "[inline_data]")
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func capturedBodyFromAudit(audit map[string]interface{}) *capturedBody {
	if audit == nil {
		return nil
	}
	body := common.Interface2String(audit["body"])
	omittedReason := common.Interface2String(audit["omitted_reason"])
	contentType := common.Interface2String(audit["content_type"])
	size := interfaceToInt(audit["size"])
	capturedBytes := interfaceToInt(audit["captured_bytes"])
	status := interfaceToInt(audit["status"])
	truncated := interfaceToBool(audit["truncated"])
	redacted := interfaceToBool(audit["redacted"])
	if body == "" && omittedReason == "" && contentType == "" && size == 0 && capturedBytes == 0 && status == 0 && !truncated && !redacted {
		return nil
	}
	return &capturedBody{
		ContentType:   contentType,
		Body:          body,
		OmittedReason: omittedReason,
		Size:          int64(size),
		CapturedBytes: capturedBytes,
		Truncated:     truncated,
		Redacted:      redacted,
		Status:        status,
	}
}

func auditMap(audit map[string]interface{}, key string) map[string]interface{} {
	value, ok := audit[key]
	if !ok || value == nil {
		return nil
	}
	if m, ok := value.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func extractResponseSummary(body string) (answer string, reasoning string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ""
	}
	if strings.Contains(body, "\ndata:") || strings.HasPrefix(body, "data:") || strings.Contains(body, "event:") {
		return extractSSESummary(body)
	}
	return extractJSONResponseSummary(body)
}

func extractSSESummary(body string) (answer string, reasoning string) {
	var answers []string
	var reasonings []string
	for _, data := range splitSSEDataLines(body) {
		if data == "" || data == "[DONE]" {
			continue
		}
		partAnswer, partReasoning := extractJSONResponseSummary(data)
		if partAnswer != "" {
			answers = append(answers, partAnswer)
		}
		if partReasoning != "" {
			reasonings = append(reasonings, partReasoning)
		}
	}
	return strings.TrimSpace(strings.Join(answers, "")), strings.TrimSpace(strings.Join(reasonings, ""))
}

func splitSSEDataLines(body string) []string {
	lines := strings.Split(body, "\n")
	data := make([]string, 0)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	return data
}

func extractJSONResponseSummary(body string) (answer string, reasoning string) {
	var root map[string]interface{}
	if err := common.UnmarshalJsonStr(body, &root); err != nil {
		return "", ""
	}

	if choices, ok := root["choices"].([]interface{}); ok {
		for _, choiceAny := range choices {
			choice, ok := choiceAny.(map[string]interface{})
			if !ok {
				continue
			}
			if message, ok := choice["message"].(map[string]interface{}); ok {
				answer += textFromResponseMessage(message)
				reasoning += firstNonEmpty(common.Interface2String(message["reasoning_content"]), common.Interface2String(message["reasoning"]))
			}
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				answer += textFromResponseMessage(delta)
				reasoning += firstNonEmpty(common.Interface2String(delta["reasoning_content"]), common.Interface2String(delta["reasoning"]))
			}
			if text := common.Interface2String(choice["text"]); text != "" {
				answer += text
			}
		}
	}

	if output, ok := root["output"].([]interface{}); ok {
		outAnswer, outReasoning := extractResponsesOutput(output)
		answer += outAnswer
		reasoning += outReasoning
	}
	if response, ok := root["response"].(map[string]interface{}); ok {
		if output, ok := response["output"].([]interface{}); ok {
			outAnswer, outReasoning := extractResponsesOutput(output)
			answer += outAnswer
			reasoning += outReasoning
		}
	}
	if delta, ok := root["delta"].(string); ok && delta != "" {
		if typ := common.Interface2String(root["type"]); strings.Contains(typ, "reasoning") {
			reasoning += delta
		} else {
			answer += delta
		}
	}
	if part, ok := root["part"].(map[string]interface{}); ok {
		if text := common.Interface2String(part["text"]); text != "" {
			reasoning += text
		}
	}
	if typ := common.Interface2String(root["type"]); strings.Contains(typ, "response.") {
		return strings.TrimSpace(answer), strings.TrimSpace(reasoning)
	}
	if candidates, ok := root["candidates"].([]interface{}); ok {
		candidateAnswer, candidateReasoning := extractGeminiCandidates(candidates)
		answer += candidateAnswer
		reasoning += candidateReasoning
	}
	if content, ok := root["content"].([]interface{}); ok {
		claudeAnswer, claudeReasoning := extractClaudeContent(content)
		answer += claudeAnswer
		reasoning += claudeReasoning
	}
	if contentBlock, ok := root["content_block"].(map[string]interface{}); ok {
		appendClaudeBlock(contentBlock, &answer, &reasoning)
	}
	if deltaBlock, ok := root["delta"].(map[string]interface{}); ok {
		appendClaudeBlock(deltaBlock, &answer, &reasoning)
	}

	return strings.TrimSpace(answer), strings.TrimSpace(reasoning)
}

func extractResponsesOutput(output []interface{}) (answer string, reasoning string) {
	for _, itemAny := range output {
		item, ok := itemAny.(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := item["content"].([]interface{})
		if !ok {
			continue
		}
		for _, contentAny := range content {
			contentItem, ok := contentAny.(map[string]interface{})
			if !ok {
				continue
			}
			text := common.Interface2String(contentItem["text"])
			typ := common.Interface2String(contentItem["type"])
			itemType := common.Interface2String(item["type"])
			if strings.Contains(typ, "reasoning") || strings.Contains(typ, "thinking") || strings.Contains(typ, "summary") || strings.Contains(itemType, "reasoning") {
				reasoning += text
			} else {
				answer += text
			}
		}
	}
	return answer, reasoning
}

func extractGeminiCandidates(candidates []interface{}) (answer string, reasoning string) {
	for _, candidateAny := range candidates {
		candidate, ok := candidateAny.(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := candidate["content"].(map[string]interface{})
		if !ok {
			continue
		}
		parts, ok := content["parts"].([]interface{})
		if !ok {
			continue
		}
		for _, partAny := range parts {
			part, ok := partAny.(map[string]interface{})
			if !ok {
				continue
			}
			text := common.Interface2String(part["text"])
			if text == "" {
				continue
			}
			if interfaceToBool(part["thought"]) {
				reasoning += text
			} else {
				answer += text
			}
		}
	}
	return answer, reasoning
}

func extractClaudeContent(content []interface{}) (answer string, reasoning string) {
	for _, blockAny := range content {
		block, ok := blockAny.(map[string]interface{})
		if !ok {
			continue
		}
		appendClaudeBlock(block, &answer, &reasoning)
	}
	return answer, reasoning
}

func appendClaudeBlock(block map[string]interface{}, answer *string, reasoning *string) {
	typ := common.Interface2String(block["type"])
	text := firstNonEmpty(common.Interface2String(block["text"]), common.Interface2String(block["delta"]), common.Interface2String(block["thinking"]))
	if text == "" {
		return
	}
	if strings.Contains(typ, "thinking") {
		*reasoning += text
		return
	}
	*answer += text
}

func textFromResponseMessage(message map[string]interface{}) string {
	if text := common.Interface2String(message["content"]); text != "" {
		return text
	}
	content, ok := message["content"].([]interface{})
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, partAny := range content {
		part, ok := partAny.(map[string]interface{})
		if !ok {
			continue
		}
		if text := common.Interface2String(part["text"]); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "")
}

func normalizeCaptureRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return captureRoleUser
	case "assistant", "model":
		return captureRoleAssistant
	case "system", "developer":
		return captureRoleSystem
	case "tool", "function":
		return captureRoleTool
	default:
		if strings.TrimSpace(role) != "" {
			return role
		}
		return "unknown"
	}
}

func anyToText(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []string:
		return strings.TrimSpace(strings.Join(v, "\n"))
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := anyToText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]interface{}:
		for _, key := range []string{"text", "content", "input", "prompt"} {
			if text := anyToText(v[key]); text != "" {
				return text
			}
		}
		if typ := common.Interface2String(v["type"]); typ != "" {
			if strings.Contains(typ, "image") || strings.Contains(typ, "audio") || strings.Contains(typ, "file") || strings.Contains(typ, "video") {
				return "[" + typ + "]"
			}
		}
		b, err := common.Marshal(v)
		if err == nil {
			return string(b)
		}
	default:
		b, err := common.Marshal(v)
		if err == nil && string(b) != "null" {
			return string(b)
		}
	}
	return ""
}

func rawMessageToText(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if err := common.Unmarshal(raw, &str); err == nil {
		return strings.TrimSpace(str)
	}
	var value interface{}
	if err := common.Unmarshal(raw, &value); err == nil {
		return anyToText(value)
	}
	return strings.TrimSpace(string(raw))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func interfaceToInt(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case int32:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	case string:
		var i int
		_, _ = fmt.Sscanf(v, "%d", &i)
		return i
	default:
		return 0
	}
}

func interfaceToBool(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}
