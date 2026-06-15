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

func TestAPIRequestLogCaptureRecordsOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.APIRequestLog{}); err != nil {
		t.Fatal(err)
	}

	oldLogDB := model.LOG_DB
	oldEnabled := common.APIRequestLogEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	model.LOG_DB = db
	common.APIRequestLogEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = true
	t.Cleanup(func() {
		model.LOG_DB = oldLogDB
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogCaptureResponse = oldCaptureResponse
		common.APIRequestLogRedactSecrets = oldRedactSecrets
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-test","api_key":"sk-secret-value"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("id", 2)
	ctx.Set("username", "alice")
	ctx.Set("token_id", 9)
	ctx.Set("token_name", "prod-token")
	ctx.Set("original_model", "gpt-test")

	StartAPIRequestLogCapture(ctx)
	if _, err := ctx.Writer.Write([]byte(`{"id":"chatcmpl-test"}`)); err != nil {
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
	if !strings.Contains(string(logs[0].RequestBody), "gpt-test") {
		t.Fatalf("expected request body to be captured, got %s", logs[0].RequestBody)
	}
	if strings.Contains(string(logs[0].RequestBody), "sk-secret-value") {
		t.Fatalf("expected request body secrets to be redacted, got %s", logs[0].RequestBody)
	}
	if !strings.Contains(string(logs[0].ResponseBody), "chatcmpl-test") {
		t.Fatalf("expected response body to be captured, got %s", logs[0].ResponseBody)
	}
}

func TestRecordAPIRequestLogForConsumeUsesConsumeLogFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.APIRequestLog{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}

	oldLogDB := model.LOG_DB
	oldEnabled := common.APIRequestLogEnabled
	oldCaptureResponse := common.APIRequestLogCaptureResponse
	oldRedactSecrets := common.APIRequestLogRedactSecrets
	model.LOG_DB = db
	common.APIRequestLogEnabled = true
	common.APIRequestLogCaptureResponse = true
	common.APIRequestLogRedactSecrets = true
	t.Cleanup(func() {
		model.LOG_DB = oldLogDB
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
	if _, err := ctx.Writer.Write([]byte(`{"id":"resp-test"}`)); err != nil {
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
	if !strings.Contains(string(logs[0].RequestBody), "hello") {
		t.Fatalf("expected request body to be captured, got %s", logs[0].RequestBody)
	}
	if !strings.Contains(string(logs[0].ResponseBody), "resp-test") {
		t.Fatalf("expected response body to be captured, got %s", logs[0].ResponseBody)
	}
}
