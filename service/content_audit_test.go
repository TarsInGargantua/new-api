package service

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAppendContentAuditOmitsRequestContent(t *testing.T) {
	oldEnabled := common.AuditContentEnabled
	oldCaptureResponse := common.AuditContentCaptureResponse
	common.AuditContentEnabled = true
	common.AuditContentCaptureResponse = false
	t.Cleanup(func() {
		common.AuditContentEnabled = oldEnabled
		common.AuditContentCaptureResponse = oldCaptureResponse
	})

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	requestBody := `{"model":"gpt-test","messages":[{"role":"user","content":"private request"}]}`
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(requestBody))
	ctx.Request.Header.Set("Content-Type", "application/json")
	t.Cleanup(func() { common.CleanupBodyStorage(ctx) })

	other := map[string]interface{}{
		"model_ratio": 1.5,
		"group_ratio": 2.0,
	}
	AppendContentAudit(ctx, other)
	appendMessageCapture(ctx, nil, other)

	require.Equal(t, 1.5, other["model_ratio"])
	require.Equal(t, 2.0, other["group_ratio"])

	audit, ok := other["audit_content"].(map[string]interface{})
	require.True(t, ok)
	requestAudit, ok := audit["request"].(map[string]interface{})
	require.True(t, ok)
	require.NotContains(t, requestAudit, "body")
	require.Equal(t, int64(len(requestBody)), requestAudit["size"])
	require.Equal(t, "request_body_not_stored", requestAudit["omitted_reason"])

	capture, ok := other[messageCaptureKey].(map[string]interface{})
	require.True(t, ok)
	require.NotContains(t, capture, "question")
	require.NotContains(t, capture, "messages")
	rawRequest, ok := capture["raw_request"].(map[string]interface{})
	require.True(t, ok)
	require.NotContains(t, rawRequest, "body")
}

func TestMessageCaptureDoesNotSerializeRequestMessages(t *testing.T) {
	capture := &messageCapture{
		ConversationID: "conversation-1",
		Question:       "private question",
		Messages: []capturedMessage{{
			Role:    captureRoleUser,
			Content: "private message",
		}},
		Answer:         "answer summary",
		ModelReasoning: "reasoning summary",
	}

	encoded := capture.toMap()
	require.Equal(t, "conversation-1", encoded["conversation_id"])
	require.Equal(t, "answer summary", encoded["answer"])
	require.Equal(t, "reasoning summary", encoded["model_reasoning"])
	require.NotContains(t, encoded, "question")
	require.NotContains(t, encoded, "messages")
}

func TestAppendContentAuditOmitsResponseBodiesButKeepsSummary(t *testing.T) {
	oldEnabled := common.AuditContentEnabled
	oldCaptureResponse := common.AuditContentCaptureResponse
	common.AuditContentEnabled = true
	common.AuditContentCaptureResponse = true
	t.Cleanup(func() {
		common.AuditContentEnabled = oldEnabled
		common.AuditContentCaptureResponse = oldCaptureResponse
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Writer.Header().Set("Content-Type", "application/json")
	StartContentAuditCapture(ctx)
	responseBody := `{"choices":[{"message":{"content":"answer","reasoning_content":"reasoning"}}]}`
	_, err := ctx.Writer.Write([]byte(responseBody))
	require.NoError(t, err)

	other := map[string]interface{}{}
	AppendContentAudit(ctx, other)
	appendMessageCapture(ctx, nil, other)

	audit := other["audit_content"].(map[string]interface{})
	responseAudit := audit["response"].(map[string]interface{})
	require.NotContains(t, responseAudit, "body")
	require.Equal(t, int64(len(responseBody)), responseAudit["size"])
	require.Equal(t, http.StatusOK, responseAudit["status"])

	capture := other[messageCaptureKey].(map[string]interface{})
	require.Equal(t, "answer", capture["answer"])
	require.Equal(t, "reasoning", capture["model_reasoning"])
	rawResponse := capture["raw_response"].(map[string]interface{})
	require.NotContains(t, rawResponse, "body")
	require.Equal(t, int64(len(responseBody)), rawResponse["size"])
}
