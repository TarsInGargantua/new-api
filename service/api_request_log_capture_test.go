package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAPIRequestLogRedactsSecretsWhenEnabled(t *testing.T) {
	body := []byte(`{"api_key":"sk-secret-value","messages":[{"content":"hello"}]}`)
	text, redacted := auditBodyToStringWithRedact(body, "application/json", true)
	if !redacted {
		t.Fatal("expected body to be marked redacted")
	}
	if strings.Contains(text, "sk-secret-value") {
		t.Fatalf("expected secret to be redacted, got %s", text)
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %s", text)
	}
}

func TestAPIRequestLogKeepsSecretsWhenDisabled(t *testing.T) {
	body := []byte(`{"api_key":"sk-secret-value"}`)
	text, redacted := auditBodyToStringWithRedact(body, "application/json", false)
	if redacted {
		t.Fatal("expected body to remain unredacted")
	}
	if !strings.Contains(text, "sk-secret-value") {
		t.Fatalf("expected original secret, got %s", text)
	}
}

func TestAPIRequestLogNonTextContentTypeIsNotAuditable(t *testing.T) {
	if isAuditableContentType("image/png") {
		t.Fatal("image/png should not be auditable")
	}
	if !isAuditableContentType("application/json; charset=utf-8") {
		t.Fatal("json should be auditable")
	}
}

func TestAPIRequestLogCaptureSkipsExcludedUsername(t *testing.T) {
	oldEnabled := common.APIRequestLogEnabled
	common.APIRequestLogEnabled = true
	common.SetCallLogExcludedUsernames("ryan")
	t.Cleanup(func() {
		common.APIRequestLogEnabled = oldEnabled
		common.SetCallLogExcludedUsernames("ryan")
	})

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[]}`))
	ctx.Set("username", "RYAN")

	StartAPIRequestLogCapture(ctx)
	if _, exists := ctx.Get(apiRequestLogWriterKey); exists {
		t.Fatal("excluded username should not start request log capture")
	}
	RecordAPIRequestLog(ctx, nil, nil)
}

func TestAPIRequestLogBodyCaptureUsesConfiguredLimit(t *testing.T) {
	oldEnabled := common.APIRequestLogEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	oldMaxBodyBytes := common.APIRequestLogMaxBodyBytes
	common.APIRequestLogEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = false
	common.APIRequestLogMaxBodyBytes = 12
	t.Cleanup(func() {
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogCaptureResponse = oldCaptureResponse
		common.APIRequestLogRedactSecrets = oldRedactSecrets
		common.APIRequestLogMaxBodyBytes = oldMaxBodyBytes
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[{"role":"user","content":"this body is intentionally long"}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	requestLog := buildRequestLogBody(ctx)
	if requestLog.omittedReason != "truncated" {
		t.Fatalf("expected truncated request body, got %+v", requestLog)
	}
	if requestLog.size <= int64(common.APIRequestLogMaxBodyBytes) {
		t.Fatalf("expected original request size to be recorded, got %d", requestLog.size)
	}
	if len(requestLog.body) > common.APIRequestLogMaxBodyBytes {
		t.Fatalf("expected captured request body to be limited, got %d", len(requestLog.body))
	}

	StartAPIRequestLogCapture(ctx)
	if _, err := ctx.Writer.Write([]byte("1234567890abcdef")); err != nil {
		t.Fatal(err)
	}
	responseLog := buildResponseLogBody(ctx)
	if responseLog.omittedReason != "truncated" {
		t.Fatalf("expected truncated response body, got %+v", responseLog)
	}
	if responseLog.size != 16 {
		t.Fatalf("expected full response size to be tracked, got %d", responseLog.size)
	}
	if len(responseLog.body) > common.APIRequestLogMaxBodyBytes {
		t.Fatalf("expected captured response body to be limited, got %d", len(responseLog.body))
	}
}

func TestAPIRequestLogTurnMetaParsesCodexAllowlistWithThreadFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	ctx.Request.Header.Set("X-Codex-Turn-Metadata", `{"session_id":"","thread_id":"thread_1","turn_id":"turn_1","window_id":"window_1","request_kind":"user","turn_started_at_unix_ms":"1784275200123","opaque_secret":"must-not-be-retained"}`)

	meta := apiRequestLogTurnMetaFromRequest(ctx, nil)
	if meta.SessionID != "thread_1" || meta.TurnID != "turn_1" || meta.WindowID != "window_1" || meta.RequestKind != "user" {
		t.Fatalf("unexpected codex turn metadata: %+v", meta)
	}
	if meta.TurnStartedAtUnixMS != 1784275200123 {
		t.Fatalf("unexpected turn start timestamp: %d", meta.TurnStartedAtUnixMS)
	}
}

func TestAPIRequestLogIncrementalSSECapturesFinalPastBodyLimit(t *testing.T) {
	oldEnabled := common.APIRequestLogEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	oldMaxBodyBytes := common.APIRequestLogMaxBodyBytes
	common.APIRequestLogEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = false
	common.APIRequestLogMaxBodyBytes = 64
	t.Cleanup(func() {
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogCaptureResponse = oldCaptureResponse
		common.APIRequestLogRedactSecrets = oldRedactSecrets
		common.APIRequestLogMaxBodyBytes = oldMaxBodyBytes
	})

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"input":"hello"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("X-Codex-Turn-Metadata", `{"session_id":"session_1","turn_id":"turn_1","window_id":"window_1","request_kind":"user","turn_started_at_unix_ms":1784275200123}`)
	ctx.Writer.Header().Set("Content-Type", "text/event-stream")
	StartAPIRequestLogCapture(ctx)

	filler := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", 256) + "\"}\n\n"
	if _, err := ctx.Writer.Write([]byte(filler)); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.Writer.Write([]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_final\",\"type\":\"message\",\"status\":\"in_progress\",\"phase\":\"final\",\"content\":[]}}\n\n")); err != nil {
		t.Fatal(err)
	}
	done := "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_final\",\"type\":\"message\",\"status\":\"completed\",\"phase\":\"final\",\"role\":\"assistant\",\"metadata\":{\"turn_id\":\"turn_1\"},\"content\":[{\"type\":\"output_text\",\"text\":\"finished\"}]}}\n\n"
	mid := len(done) / 2
	if _, err := ctx.Writer.Write([]byte(done[:mid])); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.Writer.Write([]byte(done[mid:])); err != nil {
		t.Fatal(err)
	}

	responseLog := buildResponseLogBody(ctx)
	if responseLog.omittedReason != "truncated" {
		t.Fatalf("expected bounded raw response capture to truncate, got %+v", responseLog)
	}
	result := buildAPIRequestLogItems(ctx, nil, buildRequestLogBody(ctx), responseLog)
	if result.parseStatus != model.APIRequestLogParseOK || result.parseError != "" {
		t.Fatalf("unexpected parse result: %s %s", result.parseStatus, result.parseError)
	}
	if !result.turnMeta.Completed || result.turnMeta.CompletionSignal != "responses.message.final.completed" {
		t.Fatalf("expected exact final completion past raw body limit, got %+v", result.turnMeta)
	}
	if result.turnMeta.SessionID != "session_1" || result.turnMeta.TurnID != "turn_1" || result.turnMeta.TurnStartedAtUnixMS != 1784275200123 {
		t.Fatalf("unexpected turn identifiers: %+v", result.turnMeta)
	}
	if len(result.items) != 2 {
		t.Fatalf("expected one request and one completed response item, got %d: %+v", len(result.items), result.items)
	}
	if string(result.items[1].Content) != "finished" {
		t.Fatalf("unexpected final item content: %q", result.items[1].Content)
	}
	if len(result.turnMeta.Items) != 2 {
		t.Fatalf("expected metadata aligned with normalized items, got %+v", result.turnMeta.Items)
	}
	finalMeta := result.turnMeta.Items[1]
	if finalMeta.Seq != 2 || finalMeta.ProviderItemID != "msg_final" || finalMeta.TurnID != "turn_1" || finalMeta.MessagePhase != "final" || finalMeta.Status != "completed" {
		t.Fatalf("unexpected final item metadata: %+v", finalMeta)
	}
}

func TestAPIRequestLogCaptureRecordsOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.APIRequestLog{}, &model.APIRequestLogItem{}); err != nil {
		t.Fatal(err)
	}

	oldLogDB := model.LOG_DB
	oldRequestLogDB := model.REQUEST_LOG_DB
	oldEnabled := common.APIRequestLogEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	model.LOG_DB = db
	model.REQUEST_LOG_DB = db
	common.APIRequestLogEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = true
	t.Cleanup(func() {
		model.LOG_DB = oldLogDB
		model.REQUEST_LOG_DB = oldRequestLogDB
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogCaptureResponse = oldCaptureResponse
		common.APIRequestLogRedactSecrets = oldRedactSecrets
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-test","api_key":"sk-secret-value","messages":[{"role":"user","content":"hello"}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("id", 2)
	ctx.Set("username", "alice")
	ctx.Set("token_id", 9)
	ctx.Set("token_name", "prod-token")
	ctx.Set("original_model", "gpt-test")

	StartAPIRequestLogCapture(ctx)
	if _, err := ctx.Writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"chatcmpl-test"}}]}`)); err != nil {
		t.Fatal(err)
	}
	RecordAPIRequestLog(ctx, nil, nil)
	RecordAPIRequestLog(ctx, nil, nil)

	var logs []model.APIRequestLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one request log, got %d", len(logs))
	}
	if logs[0].Username != "alice" || logs[0].TokenName != "prod-token" || logs[0].ModelName != "gpt-test" {
		t.Fatalf("unexpected request log metadata: %+v", logs[0])
	}
	var items []model.APIRequestLogItem
	if err := db.Where("log_id = ?", logs[0].Id).Order("seq asc").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected request log items to be captured")
	}
	joined := requestLogItemText(items)
	if !strings.Contains(joined, "hello") {
		t.Fatalf("expected request items to include user input, got %s", joined)
	}
	if strings.Contains(joined, "sk-secret-value") {
		t.Fatalf("expected request item secrets to be redacted, got %s", joined)
	}
	if !strings.Contains(joined, "chatcmpl-test") {
		t.Fatalf("expected response items to include response id, got %s", joined)
	}
}

func TestRecordAPIRequestLogForConsumeUsesConsumeLogFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.APIRequestLog{}, &model.APIRequestLogItem{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}

	oldLogDB := model.LOG_DB
	oldRequestLogDB := model.REQUEST_LOG_DB
	oldEnabled := common.APIRequestLogEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	model.LOG_DB = db
	model.REQUEST_LOG_DB = db
	common.APIRequestLogEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = true
	t.Cleanup(func() {
		model.LOG_DB = oldLogDB
		model.REQUEST_LOG_DB = oldRequestLogDB
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogCaptureResponse = oldCaptureResponse
		common.APIRequestLogRedactSecrets = oldRedactSecrets
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-consume","input":"hello"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("id", 7000)
	ctx.Set("username", "context-user")
	ctx.Set("token_id", 11000)
	ctx.Set("token_name", "context-token")
	ctx.Set("channel_id", 13000)
	ctx.Set("group", "context-group")
	ctx.Set("original_model", "context-model")
	StartAPIRequestLogCapture(ctx)
	if _, err := ctx.Writer.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"resp-test"}]}]}`)); err != nil {
		t.Fatal(err)
	}

	relayInfo := &relaycommon.RelayInfo{
		UserId:          7,
		TokenId:         11,
		OriginModelName: "gpt-consume",
		UsingGroup:      "vip",
		IsStream:        true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: 13,
		},
	}
	consumeLog := &model.Log{
		UserId:            17,
		Username:          "consume-user",
		TokenId:           21,
		TokenName:         "consume-token",
		ModelName:         "gpt-consume-log",
		CreatedAt:         123,
		RequestId:         "req-consume",
		UpstreamRequestId: "upstream-consume",
		IsStream:          true,
		ChannelId:         23,
		Group:             "consume-group",
		Type:              model.LogTypeConsume,
		Quota:             99,
		PromptTokens:      8,
		CompletionTokens:  9,
		Content:           "consume content",
	}
	if err := db.Create(consumeLog).Error; err != nil {
		t.Fatal(err)
	}
	RecordAPIRequestLogForConsume(ctx, relayInfo, consumeLog)
	RecordAPIRequestLogForConsume(ctx, relayInfo, consumeLog)
	RecordAPIRequestLog(ctx, relayInfo, nil)

	var logs []model.APIRequestLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one request log, got %d", len(logs))
	}
	if logs[0].UsageLogId != consumeLog.Id {
		t.Fatalf("expected usage log id %d, got %d", consumeLog.Id, logs[0].UsageLogId)
	}
	if logs[0].UserId != consumeLog.UserId || logs[0].TokenId != consumeLog.TokenId || logs[0].ChannelId != consumeLog.ChannelId {
		t.Fatalf("expected consume log identity fields, got %+v", logs[0])
	}
	if logs[0].Username != consumeLog.Username || logs[0].TokenName != consumeLog.TokenName || logs[0].ModelName != consumeLog.ModelName {
		t.Fatalf("expected consume log display fields, got %+v", logs[0])
	}
	if logs[0].CreatedAt != consumeLog.CreatedAt || logs[0].RequestId != consumeLog.RequestId || logs[0].UpstreamRequestId != consumeLog.UpstreamRequestId {
		t.Fatalf("expected consume log request fields, got %+v", logs[0])
	}
	if logs[0].Group != consumeLog.Group || !logs[0].IsStream {
		t.Fatalf("unexpected consume log metadata: %+v", logs[0])
	}
	var items []model.APIRequestLogItem
	if err := db.Where("log_id = ?", logs[0].Id).Order("seq asc").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	joined := requestLogItemText(items)
	if !strings.Contains(joined, "hello") {
		t.Fatalf("expected request items to include input, got %s", joined)
	}
	if !strings.Contains(joined, "resp-test") {
		t.Fatalf("expected response items to include response id, got %s", joined)
	}
}

func TestRecordAPIRequestLogForConsumeDoesNotBlockFinalCapture(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.APIRequestLog{}, &model.APIRequestLogItem{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}

	oldLogDB := model.LOG_DB
	oldRequestLogDB := model.REQUEST_LOG_DB
	oldEnabled := common.APIRequestLogEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	model.LOG_DB = db
	model.REQUEST_LOG_DB = db
	common.APIRequestLogEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = true
	t.Cleanup(func() {
		model.LOG_DB = oldLogDB
		model.REQUEST_LOG_DB = oldRequestLogDB
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogCaptureResponse = oldCaptureResponse
		common.APIRequestLogRedactSecrets = oldRedactSecrets
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"gpt-late","metadata":{"not_training":"only"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set(common.RequestIdKey, "req-late")
	ctx.Set(common.UpstreamRequestIdKey, "upstream-late")
	ctx.Set("id", 7000)
	ctx.Set("username", "context-user")
	ctx.Set("token_id", 11000)
	ctx.Set("token_name", "context-token")
	ctx.Set("channel_id", 13000)
	ctx.Set("group", "context-group")
	ctx.Set("original_model", "gpt-late")
	StartAPIRequestLogCapture(ctx)

	consumeLog := &model.Log{
		UserId:            17,
		Username:          "consume-user",
		TokenId:           21,
		TokenName:         "consume-token",
		ModelName:         "gpt-late",
		CreatedAt:         123,
		RequestId:         "req-late",
		UpstreamRequestId: "upstream-late",
		ChannelId:         23,
		Group:             "consume-group",
		Type:              model.LogTypeConsume,
		Quota:             99,
		PromptTokens:      8,
		CompletionTokens:  9,
	}
	if err := db.Create(consumeLog).Error; err != nil {
		t.Fatal(err)
	}
	if err := model.CreateAPIRequestLogFromConsumeLog(ctx, consumeLog); err != nil {
		t.Fatal(err)
	}
	RecordAPIRequestLogForConsume(ctx, nil, consumeLog)

	if _, err := ctx.Writer.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"late-output"}]}]}`)); err != nil {
		t.Fatal(err)
	}
	RecordAPIRequestLog(ctx, nil, nil)

	var logs []model.APIRequestLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one merged request log, got %d", len(logs))
	}
	if logs[0].UsageLogId != consumeLog.Id {
		t.Fatalf("expected merged log to keep usage log id %d, got %d", consumeLog.Id, logs[0].UsageLogId)
	}
	var items []model.APIRequestLogItem
	if err := db.Where("log_id = ?", logs[0].Id).Order("seq asc").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	joined := requestLogItemText(items)
	if !strings.Contains(joined, "late-output") {
		t.Fatalf("expected final capture to include late response item, got %s", joined)
	}
}

func TestRecordConsumeLogDefersAPIRequestLogUntilFinalCapture(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.APIRequestLog{}, &model.APIRequestLogItem{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}

	oldLogDB := model.LOG_DB
	oldRequestLogDB := model.REQUEST_LOG_DB
	oldEnabled := common.APIRequestLogEnabled
	oldLogConsumeEnabled := common.LogConsumeEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	model.LOG_DB = db
	model.REQUEST_LOG_DB = db
	common.APIRequestLogEnabled = true
	common.LogConsumeEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = true
	t.Cleanup(func() {
		model.LOG_DB = oldLogDB
		model.REQUEST_LOG_DB = oldRequestLogDB
		common.APIRequestLogEnabled = oldEnabled
		common.LogConsumeEnabled = oldLogConsumeEnabled
		common.APIRequestLogCaptureResponse = oldCaptureResponse
		common.APIRequestLogRedactSecrets = oldRedactSecrets
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-single-write","messages":[{"role":"user","content":"single-write-input"}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set(common.RequestIdKey, "req-single-write")
	ctx.Set(common.UpstreamRequestIdKey, "upstream-single-write")
	ctx.Set("id", 2)
	ctx.Set("username", "single-user")
	ctx.Set("token_id", 3)
	ctx.Set("token_name", "single-token")
	ctx.Set("original_model", "gpt-single-write")
	StartAPIRequestLogCapture(ctx)

	consumeLog := model.RecordConsumeLog(ctx, 2, model.RecordConsumeLogParams{
		ChannelId:        4,
		PromptTokens:     5,
		CompletionTokens: 6,
		ModelName:        "gpt-single-write",
		TokenName:        "single-token",
		Quota:            7,
		TokenId:          3,
		Group:            "default",
	})
	if consumeLog == nil {
		t.Fatal("expected consume log")
	}
	RecordAPIRequestLogForConsume(ctx, nil, consumeLog)

	var count int64
	if err := db.Model(&model.APIRequestLog{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected request log to be deferred until final capture, got %d", count)
	}

	if _, err := ctx.Writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"single-write-output"}}]}`)); err != nil {
		t.Fatal(err)
	}
	RecordAPIRequestLog(ctx, nil, nil)

	var logs []model.APIRequestLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one request log, got %d", len(logs))
	}
	if logs[0].UsageLogId != consumeLog.Id || logs[0].Quota != consumeLog.Quota {
		t.Fatalf("expected final request log to include consume fields, got %+v", logs[0])
	}
	var items []model.APIRequestLogItem
	if err := db.Where("log_id = ?", logs[0].Id).Order("seq asc").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	joined := requestLogItemText(items)
	if !strings.Contains(joined, "single-write-input") || !strings.Contains(joined, "single-write-output") {
		t.Fatalf("expected final items to include request and response, got %s", joined)
	}
}

func TestRecordAPIRequestLogMergesConsumeParentWithCapturedItems(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.APIRequestLog{}, &model.APIRequestLogItem{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}

	oldLogDB := model.LOG_DB
	oldRequestLogDB := model.REQUEST_LOG_DB
	oldEnabled := common.APIRequestLogEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	model.LOG_DB = db
	model.REQUEST_LOG_DB = db
	common.APIRequestLogEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = true
	t.Cleanup(func() {
		model.LOG_DB = oldLogDB
		model.REQUEST_LOG_DB = oldRequestLogDB
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogCaptureResponse = oldCaptureResponse
		common.APIRequestLogRedactSecrets = oldRedactSecrets
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-merge","messages":[{"role":"user","content":"merge-input"}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set(common.RequestIdKey, "req-merge")
	ctx.Set(common.UpstreamRequestIdKey, "upstream-merge")
	ctx.Set("id", 7000)
	ctx.Set("username", "context-user")
	ctx.Set("token_id", 11000)
	ctx.Set("token_name", "context-token")
	ctx.Set("channel_id", 13000)
	ctx.Set("group", "context-group")
	ctx.Set("original_model", "gpt-merge")
	StartAPIRequestLogCapture(ctx)
	if _, err := ctx.Writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"merge-output"}}]}`)); err != nil {
		t.Fatal(err)
	}

	consumeLog := &model.Log{
		UserId:            17,
		Username:          "consume-user",
		TokenId:           21,
		TokenName:         "consume-token",
		ModelName:         "gpt-merge",
		CreatedAt:         123,
		RequestId:         "req-merge",
		UpstreamRequestId: "upstream-merge",
		ChannelId:         23,
		Group:             "consume-group",
		Type:              model.LogTypeConsume,
		Quota:             99,
		PromptTokens:      8,
		CompletionTokens:  9,
	}
	if err := db.Create(consumeLog).Error; err != nil {
		t.Fatal(err)
	}
	if err := model.CreateAPIRequestLogFromConsumeLog(ctx, consumeLog); err != nil {
		t.Fatal(err)
	}
	RecordAPIRequestLog(ctx, nil, nil)

	var logs []model.APIRequestLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected one merged request log, got %d", len(logs))
	}
	if logs[0].UsageLogId != consumeLog.Id {
		t.Fatalf("expected merged log to keep usage log id %d, got %d", consumeLog.Id, logs[0].UsageLogId)
	}
	var items []model.APIRequestLogItem
	if err := db.Where("log_id = ?", logs[0].Id).Order("seq asc").Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	joined := requestLogItemText(items)
	if !strings.Contains(joined, "merge-input") || !strings.Contains(joined, "merge-output") {
		t.Fatalf("expected merged log items to include request and response, got %s", joined)
	}
}

func requestLogItemText(items []model.APIRequestLogItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, string(item.Content))
	}
	return strings.Join(parts, "\n")
}
