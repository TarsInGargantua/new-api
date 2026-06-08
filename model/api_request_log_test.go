package model

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAPIRequestLogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&APIRequestLog{}, &Log{}, &Ability{}, &Channel{}, &Model{}, &User{}))

	oldDB := DB
	oldLogDB := LOG_DB
	oldLogGroupCol := logGroupCol
	oldCommonGroupCol := commonGroupCol
	DB = db
	LOG_DB = db
	logGroupCol = "`group`"
	commonGroupCol = "`group`"
	t.Cleanup(func() {
		DB = oldDB
		LOG_DB = oldLogDB
		logGroupCol = oldLogGroupCol
		commonGroupCol = oldCommonGroupCol
	})
	return db
}

func TestGetLogModelNames(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	require.NoError(t, LOG_DB.Create(&Log{
		ModelName: "gpt-5.5",
		Type:      LogTypeConsume,
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		ModelName: "claude-opus-4-7",
		Type:      LogTypeConsume,
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		ModelName: "gpt-5.5",
		Type:      LogTypeConsume,
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		ModelName: "",
		Type:      LogTypeConsume,
	}).Error)
	require.NoError(t, DB.Create(&Channel{
		Models: "gpt-channel, claude-opus-4-7",
		Status: common.ChannelStatusEnabled,
	}).Error)
	require.NoError(t, DB.Create(&Ability{
		Group:     "default",
		Model:     "gpt-ability",
		ChannelId: 1,
		Enabled:   true,
	}).Error)
	require.NoError(t, DB.Create(&Model{
		ModelName: "gpt-meta",
		Status:    common.ChannelStatusEnabled,
	}).Error)

	models, err := GetLogModelNames()
	require.NoError(t, err)
	require.Equal(t, []string{"claude-opus-4-7", "gpt-5.5", "gpt-ability", "gpt-channel", "gpt-meta"}, models)
}

func TestGetUserUsageStatsPage(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	require.NoError(t, DB.Create(&User{
		Id:          2,
		Username:    "alice",
		DisplayName: "Alice",
		Group:       "default",
		Status:      common.UserStatusEnabled,
		AffCode:     "alice-aff",
	}).Error)
	require.NoError(t, DB.Create(&User{
		Id:          3,
		Username:    "bob",
		DisplayName: "Bob",
		Group:       "vip",
		Status:      common.UserStatusEnabled,
		AffCode:     "bob-aff",
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           2,
		Username:         "alice",
		Type:             LogTypeConsume,
		ModelName:        "gpt-5.5",
		Quota:            10,
		PromptTokens:     2,
		CompletionTokens: 3,
		CreatedAt:        100,
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           3,
		Username:         "bob",
		Type:             LogTypeConsume,
		ModelName:        "claude-opus-4-7",
		Quota:            20,
		PromptTokens:     4,
		CompletionTokens: 5,
		CreatedAt:        100,
	}).Error)

	rows, total, err := GetUserUsageStatsPage(nil, false, 0, 0, "gpt-5.5", 0, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].UserId)
	require.Equal(t, int64(10), rows[0].Quota)
	require.Equal(t, int64(5), rows[0].TokenUsed)

	vipIds, err := GetUserIdsByFilters("", "vip")
	require.NoError(t, err)
	rows, total, err = GetUserUsageStatsPage(vipIds, true, 0, 0, "gpt-5.5", 0, 20)
	require.NoError(t, err)
	require.Equal(t, int64(0), total)
	require.Empty(t, rows)
}

func TestAPIRequestLogCreateQueryAndDetail(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	usageLog := &Log{
		UserId:           2,
		Username:         "alice",
		Type:             LogTypeConsume,
		ModelName:        "gpt-test",
		TokenName:        "prod-token",
		Quota:            1234,
		PromptTokens:     100,
		CompletionTokens: 20,
		UseTime:          3,
		Content:          "consume detail",
		Other:            `{"foo":"bar"}`,
	}
	require.NoError(t, LOG_DB.Create(usageLog).Error)

	err := CreateAPIRequestLog(&APIRequestLog{
		UsageLogId:          usageLog.Id,
		UserId:              2,
		Username:            "alice",
		TokenId:             9,
		TokenName:           "prod-token",
		ModelName:           "gpt-test",
		CreatedAt:           100,
		RequestId:           "req-1",
		Method:              "POST",
		RequestPath:         "/v1/chat/completions",
		StatusCode:          200,
		RequestContentType:  "application/json",
		ResponseContentType: "text/event-stream",
		RequestSize:         18,
		ResponseSize:        32,
		Redacted:            true,
		RequestBody:         APIRequestLogBody(`{"prompt":"hello"}`),
		ResponseBody:        APIRequestLogBody(`data: {"ok":true}`),
		Metadata:            APIRequestLogBody(`{"relay_format":"openai"}`),
	})
	require.NoError(t, err)
	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		UserId:    3,
		Username:  "bob",
		TokenName: "other",
		ModelName: "claude-test",
		CreatedAt: 200,
	}))

	items, total, err := GetAPIRequestLogs(APIRequestLogQueryParams{
		StartTimestamp: 90,
		EndTimestamp:   150,
		TokenName:      "prod",
		ModelName:      "gpt",
		Username:       "ali",
		StartIdx:       0,
		Num:            20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	require.Equal(t, "prod-token", items[0].TokenName)
	require.Equal(t, usageLog.Id, items[0].UsageLogId)
	require.Equal(t, int64(18), items[0].RequestSize)
	require.NotNil(t, items[0].Usage)
	require.Equal(t, 1234, items[0].Usage.Quota)
	require.Equal(t, 120, items[0].Usage.TokenUsed)
	require.Empty(t, items[0].Usage.Content)

	detail, err := GetAPIRequestLogById(items[0].Id)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogBody(`{"prompt":"hello"}`), detail.RequestBody)
	require.Equal(t, APIRequestLogBody(`data: {"ok":true}`), detail.ResponseBody)
	require.NotNil(t, detail.Usage)
	require.Equal(t, "consume detail", detail.Usage.Content)
	require.Equal(t, `{"foo":"bar"}`, detail.Usage.Other)
}

func TestRecordConsumeLogSyncsAPIRequestLog(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	oldEnabled := common.APIRequestLogEnabled
	oldLogConsumeEnabled := common.LogConsumeEnabled
	common.APIRequestLogEnabled = true
	common.LogConsumeEnabled = true
	t.Cleanup(func() {
		common.APIRequestLogEnabled = oldEnabled
		common.LogConsumeEnabled = oldLogConsumeEnabled
	})

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-sync","messages":[{"role":"user","content":"hello"}]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Writer.Header().Set("Content-Type", "application/json")
	ctx.Writer.WriteHeader(http.StatusCreated)
	ctx.Set("username", "alice")
	ctx.Set(common.RequestIdKey, "req-sync")
	ctx.Set(common.UpstreamRequestIdKey, "upstream-sync")

	consumeLog := RecordConsumeLog(ctx, 2, RecordConsumeLogParams{
		ChannelId:        3,
		PromptTokens:     11,
		CompletionTokens: 7,
		ModelName:        "gpt-sync",
		TokenName:        "prod-token",
		Quota:            123,
		Content:          "consume content",
		TokenId:          5,
		UseTimeSeconds:   9,
		IsStream:         true,
		Group:            "vip",
		Other:            map[string]interface{}{"foo": "bar"},
	})
	require.NotNil(t, consumeLog)

	var requestLogs []APIRequestLog
	require.NoError(t, LOG_DB.Find(&requestLogs).Error)
	require.Len(t, requestLogs, 1)
	requestLog := requestLogs[0]
	require.Equal(t, consumeLog.Id, requestLog.UsageLogId)
	require.Equal(t, consumeLog.UserId, requestLog.UserId)
	require.Equal(t, consumeLog.Username, requestLog.Username)
	require.Equal(t, consumeLog.TokenId, requestLog.TokenId)
	require.Equal(t, consumeLog.TokenName, requestLog.TokenName)
	require.Equal(t, consumeLog.ModelName, requestLog.ModelName)
	require.Equal(t, consumeLog.CreatedAt, requestLog.CreatedAt)
	require.Equal(t, consumeLog.RequestId, requestLog.RequestId)
	require.Equal(t, consumeLog.UpstreamRequestId, requestLog.UpstreamRequestId)
	require.Equal(t, consumeLog.ChannelId, requestLog.ChannelId)
	require.Equal(t, consumeLog.Group, requestLog.Group)
	require.Equal(t, http.StatusCreated, requestLog.StatusCode)
	require.Equal(t, "/v1/chat/completions", requestLog.RequestPath)
	require.Empty(t, requestLog.RequestOmittedReason)
	require.Contains(t, string(requestLog.RequestBody), "gpt-sync")
	require.Positive(t, requestLog.RequestSize)
	require.Equal(t, "capture_pending", requestLog.ResponseOmittedReason)

	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		UsageLogId:            consumeLog.Id,
		UserId:                consumeLog.UserId,
		Username:              consumeLog.Username,
		TokenId:               consumeLog.TokenId,
		TokenName:             consumeLog.TokenName,
		ModelName:             consumeLog.ModelName,
		CreatedAt:             consumeLog.CreatedAt,
		RequestId:             consumeLog.RequestId,
		UpstreamRequestId:     consumeLog.UpstreamRequestId,
		Method:                http.MethodPost,
		RequestPath:           "/v1/chat/completions",
		StatusCode:            http.StatusOK,
		IsStream:              consumeLog.IsStream,
		ChannelId:             consumeLog.ChannelId,
		Group:                 consumeLog.Group,
		RequestContentType:    "application/json",
		RequestSize:           18,
		RequestBody:           APIRequestLogBody(`{"model":"gpt-sync"}`),
		ResponseOmittedReason: "capture_disabled",
	}))

	var count int64
	require.NoError(t, LOG_DB.Model(&APIRequestLog{}).Count(&count).Error)
	require.Equal(t, int64(1), count)

	detail, err := GetAPIRequestLogById(requestLog.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, detail.StatusCode)
	require.Equal(t, APIRequestLogBody(`{"model":"gpt-sync"}`), detail.RequestBody)
	require.NotNil(t, detail.Usage)
	require.Equal(t, 123, detail.Usage.Quota)
	require.Equal(t, 18, detail.Usage.TokenUsed)
}

func TestGetAPIRequestLogByIdHydratesBodiesFromUsageRawCapture(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	other := map[string]interface{}{
		"message_capture": map[string]interface{}{
			"raw_request": map[string]interface{}{
				"content_type": "application/json",
				"body":         `{"model":"gpt-hydrate","messages":[{"role":"user","content":"raw context"}]}`,
				"size":         76,
				"redacted":     true,
			},
			"raw_response": map[string]interface{}{
				"content_type": "application/json",
				"body":         `{"choices":[{"message":{"content":"raw answer"}}]}`,
				"size":         50,
			},
		},
	}
	otherJSON, err := common.Marshal(other)
	require.NoError(t, err)
	usageLog := &Log{
		UserId:    2,
		Username:  "alice",
		Type:      LogTypeConsume,
		ModelName: "gpt-hydrate",
		TokenName: "prod-token",
		CreatedAt: 100,
		Other:     string(otherJSON),
	}
	require.NoError(t, LOG_DB.Create(usageLog).Error)

	requestLog := &APIRequestLog{
		UsageLogId:            usageLog.Id,
		UserId:                usageLog.UserId,
		Username:              usageLog.Username,
		TokenName:             usageLog.TokenName,
		ModelName:             usageLog.ModelName,
		CreatedAt:             usageLog.CreatedAt,
		RequestOmittedReason:  apiRequestLogCapturePending,
		ResponseOmittedReason: apiRequestLogCapturePending,
		Metadata:              APIRequestLogBody(`{"request_omitted_reason":"capture_pending"}`),
	}
	require.NoError(t, LOG_DB.Create(requestLog).Error)

	detail, err := GetAPIRequestLogById(requestLog.Id)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogBody(`{"model":"gpt-hydrate","messages":[{"role":"user","content":"raw context"}]}`), detail.RequestBody)
	require.Equal(t, APIRequestLogBody(`{"choices":[{"message":{"content":"raw answer"}}]}`), detail.ResponseBody)
	require.Equal(t, "application/json", detail.RequestContentType)
	require.Equal(t, int64(76), detail.RequestSize)
	require.Empty(t, detail.RequestOmittedReason)
	require.Empty(t, detail.ResponseOmittedReason)
	require.True(t, detail.Redacted)

	var stored APIRequestLog
	require.NoError(t, LOG_DB.First(&stored, requestLog.Id).Error)
	require.Equal(t, detail.RequestBody, stored.RequestBody)
	require.Empty(t, stored.RequestOmittedReason)
	require.Contains(t, string(stored.Metadata), "usage_log.message_capture.raw_request")
}

func TestGetAPIRequestLogByIdHydratesRequestFromMessageCapture(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	other := map[string]interface{}{
		"message_capture": map[string]interface{}{
			"conversation_id": "conv-1",
			"question":        "hello context",
			"answer":          "hello answer",
			"messages": []interface{}{
				map[string]interface{}{"role": "system", "content": "be concise"},
				map[string]interface{}{"role": "user", "content": "hello context"},
			},
			"meta": map[string]interface{}{
				"model":        "gpt-message",
				"request_path": "/v1/chat/completions",
			},
		},
	}
	otherJSON, err := common.Marshal(other)
	require.NoError(t, err)
	usageLog := &Log{
		UserId:    3,
		Username:  "bob",
		Type:      LogTypeConsume,
		ModelName: "gpt-message",
		TokenName: "context-token",
		CreatedAt: 200,
		Other:     string(otherJSON),
	}
	require.NoError(t, LOG_DB.Create(usageLog).Error)

	requestLog := &APIRequestLog{
		UsageLogId:            usageLog.Id,
		UserId:                usageLog.UserId,
		Username:              usageLog.Username,
		TokenName:             usageLog.TokenName,
		ModelName:             usageLog.ModelName,
		CreatedAt:             usageLog.CreatedAt,
		RequestOmittedReason:  apiRequestLogCapturePending,
		ResponseOmittedReason: apiRequestLogCapturePending,
	}
	require.NoError(t, LOG_DB.Create(requestLog).Error)

	detail, err := GetAPIRequestLogById(requestLog.Id)
	require.NoError(t, err)
	require.Equal(t, "application/json", detail.RequestContentType)
	require.Contains(t, string(detail.RequestBody), "hello context")
	require.Contains(t, string(detail.RequestBody), "messages")
	require.Contains(t, string(detail.ResponseBody), "hello answer")
	require.Empty(t, detail.RequestOmittedReason)
	require.Empty(t, detail.ResponseOmittedReason)
	require.Contains(t, string(detail.Metadata), "usage_log.message_capture")
}

func TestGetAPIRequestLogsBackfillsMissingUsageLogs(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	oldEnabled := common.APIRequestLogEnabled
	common.APIRequestLogEnabled = true
	t.Cleanup(func() {
		common.APIRequestLogEnabled = oldEnabled
	})

	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           9,
		Username:         "backfill-user",
		Type:             LogTypeConsume,
		ModelName:        "gpt-backfill",
		TokenName:        "backfill-token",
		TokenId:          12,
		ChannelId:        34,
		Group:            "default",
		Quota:            456,
		PromptTokens:     20,
		CompletionTokens: 30,
		CreatedAt:        200,
		RequestId:        "req-backfill",
		Content:          "backfill content",
	}).Error)

	items, total, err := GetAPIRequestLogs(APIRequestLogQueryParams{
		StartTimestamp: 100,
		EndTimestamp:   300,
		ModelName:      "gpt-backfill",
		Username:       "backfill-user",
		TokenName:      "backfill-token",
		StartIdx:       0,
		Num:            20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	require.NotZero(t, items[0].UsageLogId)
	require.Equal(t, "backfill-user", items[0].Username)
	require.Equal(t, "backfill-token", items[0].TokenName)
	require.Equal(t, "gpt-backfill", items[0].ModelName)
	require.NotNil(t, items[0].Usage)
	require.Equal(t, 456, items[0].Usage.Quota)
	require.Equal(t, 50, items[0].Usage.TokenUsed)
}

func TestCreateAPIRequestLogEnsuresTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	oldLogDB := LOG_DB
	LOG_DB = db
	t.Cleanup(func() {
		LOG_DB = oldLogDB
	})

	require.False(t, LOG_DB.Migrator().HasTable(&APIRequestLog{}))
	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		Username:  "alice",
		TokenName: "prod-token",
		ModelName: "gpt-test",
		CreatedAt: 100,
	}))
	require.True(t, LOG_DB.Migrator().HasTable(&APIRequestLog{}))

	status, err := GetAPIRequestLogStorageStatus()
	require.NoError(t, err)
	require.True(t, status.HasTable)
	require.Equal(t, int64(1), status.Count)
}
