package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

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
