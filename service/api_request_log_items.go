package service

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

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
	turnMeta    APIRequestLogTurnMeta
}

type apiRequestLogItemBuilder struct {
	items            []model.APIRequestLogItem
	itemMeta         []APIRequestLogTurnItemMeta
	completed        bool
	completionSignal string
}

type apiRequestLogSSESnapshot struct {
	items            []model.APIRequestLogItem
	itemMeta         []APIRequestLogTurnItemMeta
	completed        bool
	completionSignal string
	sawSSE           bool
	parseErrors      []string
}

type apiRequestLogSSECollector struct {
	mu                  sync.Mutex
	redactSecrets       bool
	eventLimit          int
	pending             []byte
	discardLine         bool
	eventData           []string
	eventBytes          int
	eventOverflow       bool
	limitErrorRecorded  bool
	aggregateLimit      int
	aggregateWarning    bool
	sawSSE              bool
	parseErrors         []string
	seenItems           map[string]bool
	seenFallbackItems   map[string]bool
	queuedFallbackItems map[string]bool
	doneContentKeys     map[string]bool
	fallbackItems       []map[string]interface{}
	fallbackBytes       int
	fallbackWarning     bool
	encryptedSeen       map[string]bool
	answer              strings.Builder
	answerTruncated     bool
	reasoning           strings.Builder
	reasoningTruncated  bool
	genericFlushed      bool
	builder             apiRequestLogItemBuilder
}

func newAPIRequestLogSSECollector(redactSecrets bool) *apiRequestLogSSECollector {
	limit := common.APIRequestLogMaxItemBytes
	if limit <= 0 {
		limit = 4 * 1024 * 1024
	}
	return &apiRequestLogSSECollector{
		redactSecrets:       redactSecrets,
		eventLimit:          limit,
		aggregateLimit:      limit,
		seenItems:           make(map[string]bool),
		seenFallbackItems:   make(map[string]bool),
		queuedFallbackItems: make(map[string]bool),
		doneContentKeys:     make(map[string]bool),
		encryptedSeen:       make(map[string]bool),
	}
}

func (c *apiRequestLogSSECollector) Feed(data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(data) > 0 {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			c.consumeLineFragment(data)
			return
		}
		c.consumeLineFragment(data[:idx])
		if c.discardLine {
			c.discardLine = false
			c.pending = nil
		} else {
			line := string(c.pending)
			c.pending = nil
			c.consumeLine(line)
		}
		data = data[idx+1:]
	}
}

func (c *apiRequestLogSSECollector) Snapshot() apiRequestLogSSESnapshot {
	if c == nil {
		return apiRequestLogSSESnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) > 0 && !c.discardLine {
		c.consumeLine(string(c.pending))
	}
	c.pending = nil
	c.discardLine = false
	c.consumeEvent()
	for _, item := range c.fallbackItems {
		c.appendCompletedResponseItem(item, true)
	}
	c.fallbackItems = nil
	return apiRequestLogSSESnapshot{
		items:            append([]model.APIRequestLogItem(nil), c.builder.items...),
		itemMeta:         append([]APIRequestLogTurnItemMeta(nil), c.builder.itemMeta...),
		completed:        c.builder.completed,
		completionSignal: c.builder.completionSignal,
		sawSSE:           c.sawSSE,
		parseErrors:      append([]string(nil), c.parseErrors...),
	}
}

func (c *apiRequestLogSSECollector) consumeLineFragment(fragment []byte) {
	if c.discardLine || len(fragment) == 0 {
		return
	}
	if len(c.pending)+len(fragment) > c.eventLimit {
		prefixBytes := make([]byte, 0, 16)
		pendingLength := len(c.pending)
		if pendingLength > cap(prefixBytes) {
			pendingLength = cap(prefixBytes)
		}
		prefixBytes = append(prefixBytes, c.pending[:pendingLength]...)
		remaining := cap(prefixBytes) - len(prefixBytes)
		if remaining > len(fragment) {
			remaining = len(fragment)
		}
		prefixBytes = append(prefixBytes, fragment[:remaining]...)
		prefix := strings.TrimSpace(string(prefixBytes))
		if strings.HasPrefix(prefix, "data:") || strings.HasPrefix(prefix, "event:") {
			c.sawSSE = true
		}
		c.pending = nil
		c.discardLine = true
		c.markEventTooLarge()
		return
	}
	c.pending = append(c.pending, fragment...)
}

func (c *apiRequestLogSSECollector) consumeLine(line string) {
	line = strings.TrimSuffix(line, "\r")
	if line == "" {
		c.consumeEvent()
		return
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, ":") {
		return
	}
	if strings.HasPrefix(trimmed, "event:") {
		c.sawSSE = true
		return
	}
	if strings.HasPrefix(trimmed, "data:") {
		c.sawSSE = true
		data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if c.eventBytes+len(data) > c.eventLimit {
			c.markEventTooLarge()
			return
		}
		c.eventData = append(c.eventData, data)
		c.eventBytes += len(data)
	}
}

func (c *apiRequestLogSSECollector) consumeEvent() {
	if c.eventOverflow {
		c.eventData = nil
		c.eventBytes = 0
		c.eventOverflow = false
		return
	}
	if len(c.eventData) == 0 {
		return
	}
	data := strings.TrimSpace(strings.Join(c.eventData, "\n"))
	c.eventData = nil
	c.eventBytes = 0
	if data == "" || data == "[DONE]" {
		return
	}
	if c.redactSecrets {
		if redacted, _ := auditBodyToStringWithRedact(common.StringToByteSlice(data), "application/json", true); redacted != "" {
			data = redacted
		}
	}
	var root map[string]interface{}
	if err := common.UnmarshalJsonStr(data, &root); err != nil {
		if len(c.parseErrors) < 20 {
			c.parseErrors = append(c.parseErrors, err.Error())
		}
		return
	}
	typ := strings.ToLower(strings.TrimSpace(common.Interface2String(root["type"])))
	if typ == "response.output_item.done" {
		if item, ok := root["item"].(map[string]interface{}); ok {
			c.appendCompletedResponseItem(item, false)
		}
		return
	}
	if typ == "response.completed" {
		c.collectResponseCompletedFallback(root)
		return
	}
	if strings.HasPrefix(typ, "response.") {
		// response.completed ends one upstream request, not the full agent turn.
		return
	}
	partAnswer, partReasoning := extractJSONResponseSummary(data)
	c.appendGenericDelta(&c.answer, partAnswer, &c.answerTruncated)
	c.appendGenericDelta(&c.reasoning, partReasoning, &c.reasoningTruncated)
	if signal := apiRequestLogProtocolCompletionSignal(root); signal != "" {
		c.builder.markCompleted(signal)
		c.flushGenericOutput(signal)
	}
}

func (c *apiRequestLogSSECollector) markEventTooLarge() {
	c.eventData = nil
	c.eventBytes = 0
	c.eventOverflow = true
	if !c.limitErrorRecorded {
		c.limitErrorRecorded = true
		c.parseErrors = append(c.parseErrors, fmt.Sprintf("SSE event exceeds %d bytes", c.eventLimit))
	}
}

func (c *apiRequestLogSSECollector) collectResponseCompletedFallback(root map[string]interface{}) {
	response, ok := root["response"].(map[string]interface{})
	if !ok {
		return
	}
	output, ok := response["output"].([]interface{})
	if !ok {
		return
	}
	for _, itemAny := range output {
		item, ok := itemAny.(map[string]interface{})
		if !ok {
			continue
		}
		key := firstNonEmpty(
			common.Interface2String(item["id"]),
			common.Interface2String(item["call_id"]),
		)
		contentKey := apiRequestLogResponseItemContentKey(item)
		if (key != "" && c.seenItems[key]) || c.doneContentKeys[contentKey] {
			continue
		}
		fallbackKey := key
		if fallbackKey == "" {
			fallbackKey = contentKey
		}
		if c.queuedFallbackItems[fallbackKey] || c.seenFallbackItems[fallbackKey] {
			continue
		}
		itemBytes := len(valueToJSON(item))
		if itemBytes == 0 {
			continue
		}
		if itemBytes > c.aggregateLimit-c.fallbackBytes {
			c.markFallbackTooLarge()
			continue
		}
		c.queuedFallbackItems[fallbackKey] = true
		c.fallbackBytes += itemBytes
		c.fallbackItems = append(c.fallbackItems, item)
	}
}

func (c *apiRequestLogSSECollector) appendGenericDelta(builder *strings.Builder, value string, truncated *bool) {
	if value == "" || *truncated {
		return
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	remaining := c.aggregateLimit - builder.Len()
	if len(value) <= remaining {
		builder.WriteString(value)
		return
	}
	if remaining > 0 {
		builder.WriteString(apiRequestLogUTF8Prefix(value, remaining))
	}
	*truncated = true
	c.markAggregateTooLarge()
}

func apiRequestLogUTF8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 || value == "" {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func (c *apiRequestLogSSECollector) markAggregateTooLarge() {
	if c.aggregateWarning {
		return
	}
	c.aggregateWarning = true
	c.parseErrors = append(c.parseErrors, fmt.Sprintf("SSE generic output exceeds %d bytes and was truncated", c.aggregateLimit))
}

func (c *apiRequestLogSSECollector) markFallbackTooLarge() {
	if c.fallbackWarning {
		return
	}
	c.fallbackWarning = true
	c.parseErrors = append(c.parseErrors, fmt.Sprintf("SSE response.completed fallback exceeds %d cumulative bytes", c.aggregateLimit))
}

func (c *apiRequestLogSSECollector) appendCompletedResponseItem(item map[string]interface{}, fallback bool) {
	key := firstNonEmpty(
		common.Interface2String(item["id"]),
		common.Interface2String(item["call_id"]),
	)
	contentKey := apiRequestLogResponseItemContentKey(item)
	if fallback {
		if (key != "" && c.seenItems[key]) || c.doneContentKeys[contentKey] {
			return
		}
		fallbackKey := key
		if fallbackKey == "" {
			fallbackKey = contentKey
		}
		if c.seenFallbackItems[fallbackKey] {
			return
		}
		c.seenFallbackItems[fallbackKey] = true
		c.builder.appendResponsesOutputItem(item, "sse.response.completed.output", c.encryptedSeen)
		return
	}
	if key != "" && c.seenItems[key] {
		return
	}
	if key == "" && c.doneContentKeys[contentKey] {
		return
	}
	if key != "" {
		c.seenItems[key] = true
	}
	c.doneContentKeys[contentKey] = true
	c.builder.appendResponsesOutputItem(item, "sse.output_item.done", c.encryptedSeen)
}

func apiRequestLogResponseItemContentKey(item map[string]interface{}) string {
	identity := map[string]interface{}{
		"type":              item["type"],
		"call_id":           item["call_id"],
		"name":              item["name"],
		"arguments":         item["arguments"],
		"content":           item["content"],
		"summary":           item["summary"],
		"output":            item["output"],
		"text":              item["text"],
		"encrypted_content": item["encrypted_content"],
	}
	return "content:" + common.Sha1(common.StringToByteSlice(valueToJSON(identity)))
}

func (c *apiRequestLogSSECollector) flushGenericOutput(signal string) {
	if c.genericFlushed {
		return
	}
	c.genericFlushed = true
	statusMeta := map[string]interface{}{"status": "completed"}
	if text := strings.TrimSpace(c.answer.String()); text != "" {
		start := len(c.builder.items)
		c.builder.addItem(model.APIRequestLogPhaseOutput, model.APIRequestLogItemMessage, "assistant", "text", text, "", "", "sse."+signal, false, c.answerTruncated)
		c.builder.annotateAddedItems(start, statusMeta)
	}
	if text := strings.TrimSpace(c.reasoning.String()); text != "" {
		start := len(c.builder.items)
		c.builder.addItem(model.APIRequestLogPhaseOutput, model.APIRequestLogItemReasoning, "assistant", "text", text, "", "", "sse."+signal+".reasoning", false, c.reasoningTruncated)
		c.builder.annotateAddedItems(start, statusMeta)
	}
}

func BuildAPIRequestLogTrainingItems(requestBody string, responseBody string) ([]model.APIRequestLogItem, string, string) {
	builder, parseStatus, parseError := buildAPIRequestLogTrainingItems(requestBody, responseBody, nil)
	return builder.items, parseStatus, parseError
}

func buildAPIRequestLogTrainingItems(requestBody string, responseBody string, streamSnapshot *apiRequestLogSSESnapshot) (*apiRequestLogItemBuilder, string, string) {
	builder := &apiRequestLogItemBuilder{}
	var parseErrors []string
	if err := builder.appendRequestItems(requestBody); err != nil {
		parseErrors = append(parseErrors, "request: "+err.Error())
	}
	if streamSnapshot != nil && streamSnapshot.sawSSE {
		builder.appendSSESnapshot(*streamSnapshot)
		parseErrors = append(parseErrors, streamSnapshot.parseErrors...)
	} else if err := builder.appendResponseItems(responseBody); err != nil {
		parseErrors = append(parseErrors, "response: "+err.Error())
	}
	parseStatus := model.APIRequestLogParseOK
	if len(builder.items) == 0 && (strings.TrimSpace(requestBody) != "" || strings.TrimSpace(responseBody) != "") {
		parseStatus = model.APIRequestLogParseFailed
	} else if len(parseErrors) > 0 {
		parseStatus = model.APIRequestLogParsePartial
	}
	return builder, parseStatus, strings.Join(parseErrors, "; ")
}

func buildAPIRequestLogItems(c *gin.Context, relayInfo *relaycommon.RelayInfo, requestLog apiRequestLogBody, responseLog apiRequestLogBody) apiRequestLogItemBuildResult {
	var streamSnapshot *apiRequestLogSSESnapshot
	if c != nil {
		if rawWriter, exists := c.Get(apiRequestLogWriterKey); exists {
			if writer, ok := rawWriter.(*apiRequestLogWriter); ok && writer != nil {
				snapshot := writer.streamSnapshot()
				if snapshot.sawSSE {
					streamSnapshot = &snapshot
				}
			}
		}
	}
	builder, parseStatus, parseError := buildAPIRequestLogTrainingItems(requestLog.body, responseLog.body, streamSnapshot)
	turnMeta := apiRequestLogTurnMetaFromRequest(c, relayInfo)
	turnMeta.Completed = builder.completed
	turnMeta.CompletionSignal = builder.completionSignal
	turnMeta.Items = append([]APIRequestLogTurnItemMeta(nil), builder.itemMeta...)
	if turnMeta.TurnID == "" {
		turnMeta.TurnID = preferredAPIRequestLogTurnID(builder.itemMeta)
	}
	result := apiRequestLogItemBuildResult{
		apiFormat:   apiRequestLogAPIFormat(c, relayInfo),
		parseStatus: parseStatus,
		parseError:  parseError,
		items:       builder.items,
		turnMeta:    turnMeta,
	}
	return result
}

func preferredAPIRequestLogTurnID(items []APIRequestLogTurnItemMeta) string {
	for _, item := range items {
		if item.MessagePhase == "final" && item.Status == "completed" && item.TurnID != "" {
			return item.TurnID
		}
	}
	for idx := len(items) - 1; idx >= 0; idx-- {
		if items[idx].TurnID != "" {
			return items[idx].TurnID
		}
	}
	return ""
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
	b.observeCompletion(root)
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
	start := len(b.items)
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
	b.annotateAddedItems(start, m)
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
		start := len(b.items)
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
		b.annotateAddedItems(start, msg)
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
	collector := newAPIRequestLogSSECollector(false)
	collector.Feed(common.StringToByteSlice(body))
	snapshot := collector.Snapshot()
	b.appendSSESnapshot(snapshot)
	if len(snapshot.parseErrors) > 0 {
		return errors.New(strings.Join(snapshot.parseErrors, "; "))
	}
	return nil
}

func (b *apiRequestLogItemBuilder) appendSSESnapshot(snapshot apiRequestLogSSESnapshot) {
	for idx, item := range snapshot.items {
		item.Id = 0
		item.LogId = 0
		item.Seq = len(b.items) + 1
		b.items = append(b.items, item)
		meta := APIRequestLogTurnItemMeta{Seq: item.Seq}
		if idx < len(snapshot.itemMeta) {
			meta = snapshot.itemMeta[idx]
			meta.Seq = item.Seq
		}
		b.itemMeta = append(b.itemMeta, meta)
	}
	if snapshot.completed {
		b.markCompleted(snapshot.completionSignal)
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
		start := len(b.items)
		callSource := fmt.Sprintf("%s[%d]", source, idx)
		name := common.Interface2String(call["name"])
		if fn, ok := call["function"].(map[string]interface{}); ok {
			name = firstNonEmpty(name, common.Interface2String(fn["name"]))
		}
		b.addJSON(phase, model.APIRequestLogItemToolCall, "assistant", valueToJSON(call), firstNonEmpty(common.Interface2String(call["id"]), common.Interface2String(call["call_id"])), name, callSource)
		b.annotateAddedItems(start, call)
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
	start := len(b.items)
	defer b.annotateAddedItems(start, item)
	if apiRequestLogResponseItemCompleted(item) {
		b.markCompleted("responses.message.final.completed")
	}
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
	item := model.APIRequestLogItem{
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
	}
	b.items = append(b.items, item)
	b.itemMeta = append(b.itemMeta, APIRequestLogTurnItemMeta{Seq: item.Seq})
}

func (b *apiRequestLogItemBuilder) annotateAddedItems(start int, source map[string]interface{}) {
	if start < 0 || start >= len(b.itemMeta) || source == nil {
		return
	}
	meta := apiRequestLogTurnItemMetaFromMap(source)
	for idx := start; idx < len(b.itemMeta); idx++ {
		if b.itemMeta[idx].ProviderItemID == "" {
			b.itemMeta[idx].ProviderItemID = meta.ProviderItemID
		}
		if b.itemMeta[idx].TurnID == "" {
			b.itemMeta[idx].TurnID = meta.TurnID
		}
		if b.itemMeta[idx].MessagePhase == "" {
			b.itemMeta[idx].MessagePhase = meta.MessagePhase
		}
		if b.itemMeta[idx].Status == "" {
			b.itemMeta[idx].Status = meta.Status
		}
	}
}

func apiRequestLogTurnItemMetaFromMap(item map[string]interface{}) APIRequestLogTurnItemMeta {
	return APIRequestLogTurnItemMeta{
		ProviderItemID: firstNonEmpty(
			common.Interface2String(item["id"]),
			common.Interface2String(item["item_id"]),
			common.Interface2String(item["call_id"]),
		),
		TurnID:       apiRequestLogTurnIDFromItem(item),
		MessagePhase: strings.TrimSpace(common.Interface2String(item["phase"])),
		Status:       strings.TrimSpace(common.Interface2String(item["status"])),
	}
}

func apiRequestLogTurnIDFromItem(item map[string]interface{}) string {
	if item == nil {
		return ""
	}
	if turnID := strings.TrimSpace(common.Interface2String(item["turn_id"])); turnID != "" {
		return turnID
	}
	for _, key := range []string{"metadata", "internal_chat_message_metadata_passthrough"} {
		value := item[key]
		if metadata, ok := value.(map[string]interface{}); ok {
			if turnID := strings.TrimSpace(common.Interface2String(metadata["turn_id"])); turnID != "" {
				return turnID
			}
			continue
		}
		if raw := strings.TrimSpace(common.Interface2String(value)); raw != "" {
			var metadata map[string]interface{}
			if common.UnmarshalJsonStr(raw, &metadata) == nil {
				if turnID := strings.TrimSpace(common.Interface2String(metadata["turn_id"])); turnID != "" {
					return turnID
				}
			}
		}
	}
	return ""
}

func (b *apiRequestLogItemBuilder) observeCompletion(root map[string]interface{}) {
	if root == nil {
		return
	}
	if item, ok := root["item"].(map[string]interface{}); ok && apiRequestLogResponseItemCompleted(item) {
		b.markCompleted("responses.message.final.completed")
	}
	if signal := apiRequestLogProtocolCompletionSignal(root); signal != "" {
		b.markCompleted(signal)
	}
}

func (b *apiRequestLogItemBuilder) markCompleted(signal string) {
	if strings.TrimSpace(signal) == "" {
		return
	}
	if !b.completed || signal == "responses.message.final.completed" {
		b.completed = true
		b.completionSignal = signal
	}
}

func apiRequestLogResponseItemCompleted(item map[string]interface{}) bool {
	if item == nil || !strings.Contains(strings.ToLower(common.Interface2String(item["type"])), "message") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(common.Interface2String(item["phase"])), "final") &&
		strings.EqualFold(strings.TrimSpace(common.Interface2String(item["status"])), "completed")
}

func apiRequestLogProtocolCompletionSignal(root map[string]interface{}) string {
	if root == nil {
		return ""
	}
	typ := strings.ToLower(strings.TrimSpace(common.Interface2String(root["type"])))
	if typ == "message_stop" {
		return "claude.message_stop"
	}
	if reason := strings.TrimSpace(common.Interface2String(root["stop_reason"])); reason != "" {
		return "claude.stop_reason:" + reason
	}
	if choices, ok := root["choices"].([]interface{}); ok {
		for _, choiceAny := range choices {
			choice, ok := choiceAny.(map[string]interface{})
			if !ok {
				continue
			}
			if reason := strings.TrimSpace(firstNonEmpty(common.Interface2String(choice["finish_reason"]), common.Interface2String(choice["finishReason"]))); reason != "" {
				return "chat.finish_reason:" + reason
			}
		}
	}
	if candidates, ok := root["candidates"].([]interface{}); ok {
		for _, candidateAny := range candidates {
			candidate, ok := candidateAny.(map[string]interface{})
			if !ok {
				continue
			}
			if reason := strings.TrimSpace(firstNonEmpty(common.Interface2String(candidate["finishReason"]), common.Interface2String(candidate["finish_reason"]))); reason != "" {
				return "gemini.finish_reason:" + reason
			}
		}
	}
	return ""
}

func normalizeTrainingRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system":
		return "system"
	case "developer":
		return "developer"
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
