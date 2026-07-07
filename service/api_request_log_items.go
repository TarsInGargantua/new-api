package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

type apiRequestLogItemBuildResult struct {
	items       []model.APIRequestLogItem
	apiFormat   string
	parseStatus string
	parseError  string
}

type apiRequestLogItemBuilder struct {
	items []model.APIRequestLogItem
}

func BuildAPIRequestLogTrainingItems(requestBody string, responseBody string) ([]model.APIRequestLogItem, string, string) {
	builder := &apiRequestLogItemBuilder{}
	var parseErrors []string
	if err := builder.appendRequestItems(requestBody); err != nil {
		parseErrors = append(parseErrors, "request: "+err.Error())
	}
	if err := builder.appendResponseItems(responseBody); err != nil {
		parseErrors = append(parseErrors, "response: "+err.Error())
	}
	parseStatus := model.APIRequestLogParseOK
	if len(builder.items) == 0 && (strings.TrimSpace(requestBody) != "" || strings.TrimSpace(responseBody) != "") {
		parseStatus = model.APIRequestLogParseFailed
	} else if len(parseErrors) > 0 {
		parseStatus = model.APIRequestLogParsePartial
	}
	return builder.items, parseStatus, strings.Join(parseErrors, "; ")
}

func buildAPIRequestLogItems(c *gin.Context, relayInfo *relaycommon.RelayInfo, requestLog apiRequestLogBody, responseLog apiRequestLogBody) apiRequestLogItemBuildResult {
	items, parseStatus, parseError := BuildAPIRequestLogTrainingItems(requestLog.body, responseLog.body)
	result := apiRequestLogItemBuildResult{
		apiFormat:   apiRequestLogAPIFormat(c, relayInfo),
		parseStatus: parseStatus,
		parseError:  parseError,
		items:       items,
	}
	return result
}

func apiRequestLogAPIFormat(c *gin.Context, relayInfo *relaycommon.RelayInfo) string {
	if relayInfo != nil {
		if final := relayInfo.GetFinalRequestRelayFormat(); final != "" {
			return string(final)
		}
		if relayInfo.RelayFormat != "" {
			return string(relayInfo.RelayFormat)
		}
	}
	if c != nil && c.Request != nil && c.Request.URL != nil {
		path := c.Request.URL.Path
		switch {
		case strings.Contains(path, "/responses"):
			return "responses"
		case strings.Contains(path, "/chat/completions"):
			return "chat_completions"
		case strings.Contains(path, "/messages"):
			return "claude_messages"
		case strings.Contains(path, "/generateContent"):
			return "gemini"
		}
	}
	return ""
}

func (b *apiRequestLogItemBuilder) appendRequestItems(body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	var root map[string]interface{}
	if err := common.UnmarshalJsonStr(body, &root); err != nil {
		b.addRaw(model.APIRequestLogPhaseInput, body, "request_body")
		return err
	}

	b.appendInstruction(root, "system", "system")
	b.appendInstruction(root, "instruction", "instruction")
	b.appendInstruction(root, "instructions", "instructions")
	b.appendTextLikeInput(root, "prompt")
	b.appendTextLikeInput(root, "input")
	b.appendMessages(root["messages"], "messages")
	b.appendGeminiContents(root)
	b.appendToolSpecs(root)
	return nil
}

func (b *apiRequestLogItemBuilder) appendResponseItems(body string) error {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil
	}
	if strings.Contains(body, "\ndata:") || strings.HasPrefix(body, "data:") || strings.Contains(body, "event:") {
		return b.appendSSEResponseItems(body)
	}
	var root map[string]interface{}
	if err := common.UnmarshalJsonStr(body, &root); err != nil {
		b.addRaw(model.APIRequestLogPhaseOutput, body, "response_body")
		return err
	}
	b.appendJSONResponseItems(root, "response")
	return nil
}

func (b *apiRequestLogItemBuilder) appendInstruction(root map[string]interface{}, key string, source string) {
	value, exists := root[key]
	if !exists {
		return
	}
	if text := valueToText(value); text != "" {
		b.addText(model.APIRequestLogPhaseInput, model.APIRequestLogItemMessage, "system", text, "", "", source)
	}
}

func (b *apiRequestLogItemBuilder) appendTextLikeInput(root map[string]interface{}, key string) {
	value, exists := root[key]
	if !exists {
		return
	}
	switch v := value.(type) {
	case []interface{}:
		for idx, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				b.appendInputObject(m, fmt.Sprintf("%s[%d]", key, idx))
				continue
			}
			if text := valueToText(item); text != "" {
				b.addText(model.APIRequestLogPhaseInput, model.APIRequestLogItemMessage, "user", text, "", "", fmt.Sprintf("%s[%d]", key, idx))
			}
		}
	default:
		if text := valueToText(v); text != "" {
			b.addText(model.APIRequestLogPhaseInput, model.APIRequestLogItemMessage, "user", text, "", "", key)
		}
	}
}

func (b *apiRequestLogItemBuilder) appendInputObject(m map[string]interface{}, source string) {
	role := normalizeTrainingRole(common.Interface2String(m["role"]))
	if role == "" {
		role = "user"
	}
	if m["type"] != nil && strings.Contains(common.Interface2String(m["type"]), "tool") {
		role = "tool"
	}
	if content := valueToText(m["content"]); content != "" {
		itemType := model.APIRequestLogItemMessage
		toolCallId := common.Interface2String(m["tool_call_id"])
		if role == "tool" {
			itemType = model.APIRequestLogItemToolResult
			toolCallId = firstNonEmpty(toolCallId, common.Interface2String(m["call_id"]))
		}
		b.addText(model.APIRequestLogPhaseInput, itemType, role, content, toolCallId, common.Interface2String(m["name"]), source)
	}
	if output := valueToText(m["output"]); output != "" {
		b.addText(model.APIRequestLogPhaseInput, model.APIRequestLogItemToolResult, "tool", output, common.Interface2String(m["call_id"]), common.Interface2String(m["name"]), source+".output")
	}
}

func (b *apiRequestLogItemBuilder) appendMessages(value interface{}, source string) {
	messages, ok := value.([]interface{})
	if !ok {
		return
	}
	for idx, item := range messages {
		msg, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		role := normalizeTrainingRole(common.Interface2String(msg["role"]))
		itemSource := fmt.Sprintf("%s[%d]", source, idx)
		toolCallId := common.Interface2String(msg["tool_call_id"])
		name := common.Interface2String(msg["name"])
		itemType := model.APIRequestLogItemMessage
		if role == "tool" {
			itemType = model.APIRequestLogItemToolResult
		}
		if text := valueToText(msg["content"]); text != "" {
			b.addText(model.APIRequestLogPhaseInput, itemType, role, text, toolCallId, name, itemSource)
		}
		if reasoning := firstNonEmpty(common.Interface2String(msg["reasoning_content"]), common.Interface2String(msg["reasoning"])); reasoning != "" {
			b.addText(model.APIRequestLogPhaseInput, model.APIRequestLogItemReasoning, role, reasoning, "", "", itemSource+".reasoning")
		}
		b.appendToolCalls(model.APIRequestLogPhaseInput, msg["tool_calls"], itemSource+".tool_calls")
	}
}

func (b *apiRequestLogItemBuilder) appendClaudeMessages(root map[string]interface{}) {
	if system := valueToText(root["system"]); system != "" {
		b.addText(model.APIRequestLogPhaseInput, model.APIRequestLogItemMessage, "system", system, "", "", "system")
	}
	b.appendMessages(root["messages"], "messages")
}

func (b *apiRequestLogItemBuilder) appendGeminiContents(root map[string]interface{}) {
	if systemInstruction, ok := root["systemInstruction"].(map[string]interface{}); ok {
		if text := valueToText(systemInstruction["parts"]); text != "" {
			b.addText(model.APIRequestLogPhaseInput, model.APIRequestLogItemMessage, "system", text, "", "", "systemInstruction")
		}
	}
	contents, ok := root["contents"].([]interface{})
	if !ok {
		return
	}
	for idx, item := range contents {
		content, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		role := normalizeTrainingRole(common.Interface2String(content["role"]))
		if role == "" {
			role = "user"
		}
		source := fmt.Sprintf("contents[%d]", idx)
		if text := valueToText(content["parts"]); text != "" {
			b.addText(model.APIRequestLogPhaseInput, model.APIRequestLogItemMessage, role, text, "", "", source)
		}
		b.appendGeminiFunctionParts(model.APIRequestLogPhaseInput, content["parts"], source)
	}
}

func (b *apiRequestLogItemBuilder) appendToolSpecs(root map[string]interface{}) {
	for _, key := range []string{"tools", "functions"} {
		value, exists := root[key]
		if !exists {
			continue
		}
		if content := valueToJSON(value); content != "" {
			b.addJSON(model.APIRequestLogPhaseInput, model.APIRequestLogItemToolSpec, "", content, "", "", key)
		}
	}
}

func (b *apiRequestLogItemBuilder) appendSSEResponseItems(body string) error {
	var answer strings.Builder
	var reasoning strings.Builder
	var parseErrors []string
	encryptedSeen := map[string]bool{}
	for _, data := range splitSSEDataLines(body) {
		if data == "" || data == "[DONE]" {
			continue
		}
		var root map[string]interface{}
		if err := common.UnmarshalJsonStr(data, &root); err != nil {
			parseErrors = append(parseErrors, err.Error())
			continue
		}
		partAnswer, partReasoning := extractJSONResponseSummary(data)
		answer.WriteString(partAnswer)
		reasoning.WriteString(partReasoning)
		b.appendResponseEventItems(root, encryptedSeen)
	}
	if text := strings.TrimSpace(answer.String()); text != "" {
		b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemMessage, "assistant", text, "", "", "sse.output_text")
	}
	if text := strings.TrimSpace(reasoning.String()); text != "" {
		b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemReasoning, "assistant", text, "", "", "sse.reasoning")
	}
	if len(parseErrors) > 0 {
		return errors.New(strings.Join(parseErrors, "; "))
	}
	return nil
}

func (b *apiRequestLogItemBuilder) appendResponseEventItems(root map[string]interface{}, encryptedSeen map[string]bool) {
	if item, ok := root["item"].(map[string]interface{}); ok {
		b.appendResponsesOutputItem(item, "sse.item", encryptedSeen)
	}
	if response, ok := root["response"].(map[string]interface{}); ok {
		if tools := valueToJSON(response["tools"]); tools != "" {
			b.addJSON(model.APIRequestLogPhaseOutput, model.APIRequestLogItemToolSpec, "", tools, "", "", "response.created.tools")
		}
	}
	if typ := common.Interface2String(root["type"]); strings.Contains(typ, "tool_call") {
		b.addJSON(model.APIRequestLogPhaseOutput, model.APIRequestLogItemToolCall, "assistant", valueToJSON(root), common.Interface2String(root["call_id"]), common.Interface2String(root["name"]), "sse."+typ)
	}
}

func (b *apiRequestLogItemBuilder) appendJSONResponseItems(root map[string]interface{}, source string) {
	if choices, ok := root["choices"].([]interface{}); ok {
		for idx, choiceAny := range choices {
			choice, ok := choiceAny.(map[string]interface{})
			if !ok {
				continue
			}
			itemSource := fmt.Sprintf("%s.choices[%d]", source, idx)
			if message, ok := choice["message"].(map[string]interface{}); ok {
				b.appendResponseMessage(message, itemSource+".message")
			}
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				b.appendResponseMessage(delta, itemSource+".delta")
			}
			if text := common.Interface2String(choice["text"]); text != "" {
				b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemMessage, "assistant", text, "", "", itemSource+".text")
			}
		}
	}
	if output, ok := root["output"].([]interface{}); ok {
		b.appendResponsesOutput(output, source+".output")
	}
	if response, ok := root["response"].(map[string]interface{}); ok {
		if output, ok := response["output"].([]interface{}); ok {
			b.appendResponsesOutput(output, source+".response.output")
		}
	}
	if candidates, ok := root["candidates"].([]interface{}); ok {
		b.appendGeminiCandidates(candidates, source+".candidates")
	}
	if content, ok := root["content"].([]interface{}); ok {
		b.appendClaudeContent(content, source+".content")
	}
}

func (b *apiRequestLogItemBuilder) appendResponseMessage(message map[string]interface{}, source string) {
	if text := textFromResponseMessage(message); text != "" {
		b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemMessage, "assistant", text, "", common.Interface2String(message["name"]), source)
	}
	if reasoning := firstNonEmpty(common.Interface2String(message["reasoning_content"]), common.Interface2String(message["reasoning"])); reasoning != "" {
		b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemReasoning, "assistant", reasoning, "", "", source+".reasoning")
	}
	b.appendToolCalls(model.APIRequestLogPhaseOutput, message["tool_calls"], source+".tool_calls")
}

func (b *apiRequestLogItemBuilder) appendToolCalls(phase string, value interface{}, source string) {
	calls, ok := value.([]interface{})
	if !ok {
		return
	}
	for idx, callAny := range calls {
		call, ok := callAny.(map[string]interface{})
		if !ok {
			continue
		}
		callSource := fmt.Sprintf("%s[%d]", source, idx)
		name := common.Interface2String(call["name"])
		if fn, ok := call["function"].(map[string]interface{}); ok {
			name = firstNonEmpty(name, common.Interface2String(fn["name"]))
		}
		b.addJSON(phase, model.APIRequestLogItemToolCall, "assistant", valueToJSON(call), firstNonEmpty(common.Interface2String(call["id"]), common.Interface2String(call["call_id"])), name, callSource)
	}
}

func (b *apiRequestLogItemBuilder) appendResponsesOutput(output []interface{}, source string) {
	encryptedSeen := map[string]bool{}
	for idx, itemAny := range output {
		item, ok := itemAny.(map[string]interface{})
		if !ok {
			continue
		}
		b.appendResponsesOutputItem(item, fmt.Sprintf("%s[%d]", source, idx), encryptedSeen)
	}
}

func (b *apiRequestLogItemBuilder) appendResponsesOutputItem(item map[string]interface{}, source string, encryptedSeen map[string]bool) {
	itemType := common.Interface2String(item["type"])
	switch {
	case strings.Contains(itemType, "reasoning"):
		if text := responsesOutputContentText(item, true); text != "" {
			b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemReasoning, "assistant", text, "", "", source)
		}
		if encrypted := common.Interface2String(item["encrypted_content"]); encrypted != "" && !encryptedSeen[encrypted] {
			encryptedSeen[encrypted] = true
			b.addEncrypted(model.APIRequestLogPhaseOutput, model.APIRequestLogItemReasoning, "assistant", encrypted, "", "", source+".encrypted_content")
		}
	case strings.Contains(itemType, "function_call") || strings.Contains(itemType, "tool_call"):
		b.addJSON(model.APIRequestLogPhaseOutput, model.APIRequestLogItemToolCall, "assistant", valueToJSON(item), firstNonEmpty(common.Interface2String(item["call_id"]), common.Interface2String(item["id"])), common.Interface2String(item["name"]), source)
	case strings.Contains(itemType, "message"):
		if text := responsesOutputContentText(item, false); text != "" {
			b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemMessage, "assistant", text, "", "", source)
		}
	default:
		if text := responsesOutputContentText(item, false); text != "" {
			b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemMessage, normalizeTrainingRole(common.Interface2String(item["role"])), text, "", "", source)
		}
	}
}

func responsesOutputContentText(item map[string]interface{}, reasoningOnly bool) string {
	content, ok := item["content"].([]interface{})
	if !ok {
		if summary, ok := item["summary"].([]interface{}); ok {
			content = summary
		} else {
			return ""
		}
	}
	parts := make([]string, 0, len(content))
	for _, contentAny := range content {
		m, ok := contentAny.(map[string]interface{})
		if !ok {
			continue
		}
		typ := common.Interface2String(m["type"])
		isReasoning := strings.Contains(typ, "reasoning") || strings.Contains(typ, "summary")
		if reasoningOnly != isReasoning && reasoningOnly {
			continue
		}
		if text := firstNonEmpty(common.Interface2String(m["text"]), common.Interface2String(m["delta"]), common.Interface2String(m["summary_text"])); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

func (b *apiRequestLogItemBuilder) appendClaudeContent(content []interface{}, source string) {
	for idx, blockAny := range content {
		block, ok := blockAny.(map[string]interface{})
		if !ok {
			continue
		}
		typ := common.Interface2String(block["type"])
		itemSource := fmt.Sprintf("%s[%d]", source, idx)
		switch {
		case strings.Contains(typ, "thinking"):
			if text := firstNonEmpty(common.Interface2String(block["thinking"]), common.Interface2String(block["text"])); text != "" {
				b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemReasoning, "assistant", text, "", "", itemSource)
			}
		case strings.Contains(typ, "tool_use"):
			b.addJSON(model.APIRequestLogPhaseOutput, model.APIRequestLogItemToolCall, "assistant", valueToJSON(block), common.Interface2String(block["id"]), common.Interface2String(block["name"]), itemSource)
		default:
			if text := common.Interface2String(block["text"]); text != "" {
				b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemMessage, "assistant", text, "", "", itemSource)
			}
		}
	}
}

func (b *apiRequestLogItemBuilder) appendGeminiCandidates(candidates []interface{}, source string) {
	for idx, candidateAny := range candidates {
		candidate, ok := candidateAny.(map[string]interface{})
		if !ok {
			continue
		}
		content, _ := candidate["content"].(map[string]interface{})
		if content == nil {
			continue
		}
		itemSource := fmt.Sprintf("%s[%d]", source, idx)
		if text := valueToText(content["parts"]); text != "" {
			b.addText(model.APIRequestLogPhaseOutput, model.APIRequestLogItemMessage, "assistant", text, "", "", itemSource)
		}
		b.appendGeminiFunctionParts(model.APIRequestLogPhaseOutput, content["parts"], itemSource)
	}
}

func (b *apiRequestLogItemBuilder) appendGeminiFunctionParts(phase string, value interface{}, source string) {
	parts, ok := value.([]interface{})
	if !ok {
		return
	}
	for idx, partAny := range parts {
		part, ok := partAny.(map[string]interface{})
		if !ok {
			continue
		}
		itemSource := fmt.Sprintf("%s.parts[%d]", source, idx)
		if call, ok := part["functionCall"].(map[string]interface{}); ok {
			b.addJSON(phase, model.APIRequestLogItemToolCall, "assistant", valueToJSON(call), "", common.Interface2String(call["name"]), itemSource+".functionCall")
		}
		if resp, ok := part["functionResponse"].(map[string]interface{}); ok {
			b.addJSON(phase, model.APIRequestLogItemToolResult, "tool", valueToJSON(resp), "", common.Interface2String(resp["name"]), itemSource+".functionResponse")
		}
	}
}

func (b *apiRequestLogItemBuilder) addRaw(phase string, content string, source string) {
	b.addItem(phase, model.APIRequestLogItemRaw, "", "text", content, "", "", source, false, false)
}

func (b *apiRequestLogItemBuilder) addText(phase string, itemType string, role string, content string, toolCallId string, name string, source string) {
	b.addItem(phase, itemType, role, "text", strings.TrimSpace(content), toolCallId, name, source, false, false)
}

func (b *apiRequestLogItemBuilder) addJSON(phase string, itemType string, role string, content string, toolCallId string, name string, source string) {
	b.addItem(phase, itemType, role, "json", content, toolCallId, name, source, false, false)
}

func (b *apiRequestLogItemBuilder) addEncrypted(phase string, itemType string, role string, content string, toolCallId string, name string, source string) {
	b.addItem(phase, itemType, role, "encrypted", content, toolCallId, name, source, false, false)
}

func (b *apiRequestLogItemBuilder) addItem(phase string, itemType string, role string, contentType string, content string, toolCallId string, name string, source string, redacted bool, truncated bool) {
	if strings.TrimSpace(content) == "" {
		return
	}
	b.items = append(b.items, model.APIRequestLogItem{
		Seq:         len(b.items) + 1,
		Phase:       phase,
		ItemType:    itemType,
		Role:        normalizeTrainingRole(role),
		ContentType: contentType,
		Content:     model.APIRequestLogBody(content),
		ToolCallId:  toolCallId,
		Name:        name,
		Source:      source,
		Redacted:    redacted,
		Truncated:   truncated,
	})
}

func normalizeTrainingRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		return "system"
	case "user", "human":
		return "user"
	case "assistant", "model":
		return "assistant"
	case "tool", "function":
		return "tool"
	default:
		return strings.TrimSpace(role)
	}
}

func valueToText(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := valueToText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]interface{}:
		for _, key := range []string{"text", "content", "input", "output"} {
			if text := valueToText(v[key]); text != "" {
				return text
			}
		}
		if v["functionCall"] != nil || v["functionResponse"] != nil || v["tool_calls"] != nil {
			return ""
		}
		return valueToJSON(v)
	default:
		return strings.TrimSpace(common.Interface2String(v))
	}
}

func valueToJSON(value interface{}) string {
	if value == nil {
		return ""
	}
	encoded, err := common.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}
