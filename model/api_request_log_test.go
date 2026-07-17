package model

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAPIRequestLogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	resetAPIRequestLogItemQueueForTest()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&APIRequestLog{}, &APIRequestLogItem{}, &Log{}, &Ability{}, &Channel{}, &Model{}, &User{}, &QuotaData{}))

	oldDB := DB
	oldLogDB := LOG_DB
	oldRequestLogDB := REQUEST_LOG_DB
	oldLogGroupCol := logGroupCol
	oldCommonGroupCol := commonGroupCol
	DB = db
	LOG_DB = db
	REQUEST_LOG_DB = db
	logGroupCol = "`group`"
	commonGroupCol = "`group`"
	t.Cleanup(func() {
		resetAPIRequestLogItemQueueForTest()
		DB = oldDB
		LOG_DB = oldLogDB
		REQUEST_LOG_DB = oldRequestLogDB
		logGroupCol = oldLogGroupCol
		commonGroupCol = oldCommonGroupCol
	})
	return db
}

func resetAPIRequestLogItemQueueForTest() {
	apiRequestLogItemQueueMu.Lock()
	if apiRequestLogItemQueue != nil {
		close(apiRequestLogItemQueue)
		apiRequestLogItemQueue = nil
	}
	apiRequestLogItemQueueMu.Unlock()
	atomic.StoreInt64(&apiRequestLogQueuedItemBytes, 0)
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

	err := CreateAPIRequestLog(&APIRequestLog{
		UsageLogId:          10,
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
		Quota:               1234,
		PromptTokens:        100,
		CompletionTokens:    20,
		TokenUsed:           120,
		UseTime:             3,
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("hello")},
			{Seq: 2, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: APIRequestLogBody("ok")},
		},
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
	require.Equal(t, 10, items[0].UsageLogId)
	require.Equal(t, int64(18), items[0].RequestSize)
	require.NotNil(t, items[0].Usage)
	require.Equal(t, 1234, items[0].Usage.Quota)
	require.Equal(t, 120, items[0].Usage.TokenUsed)
	require.Empty(t, items[0].Usage.Content)

	detail, err := GetAPIRequestLogById(items[0].Id)
	require.NoError(t, err)
	require.Empty(t, detail.RequestBody)
	require.Empty(t, detail.ResponseBody)
	require.Len(t, detail.Items, 2)
	require.Equal(t, APIRequestLogBody("hello"), detail.Items[0].Content)
	require.Equal(t, APIRequestLogBody("ok"), detail.Items[1].Content)
	require.NotNil(t, detail.Usage)
	require.Equal(t, 1234, detail.Usage.Quota)
}

func TestCreateAPIRequestLogAsyncItems(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	oldAsync := common.APIRequestLogAsyncWrite
	oldQueueSize := common.APIRequestLogQueueSize
	oldWorkers := common.APIRequestLogWorkers
	oldMaxItemBytes := common.APIRequestLogMaxItemBytes
	oldMaxQueueBytes := common.APIRequestLogMaxQueueBytes
	common.APIRequestLogAsyncWrite = true
	common.APIRequestLogQueueSize = 4
	common.APIRequestLogWorkers = 1
	common.APIRequestLogMaxItemBytes = 1024
	common.APIRequestLogMaxQueueBytes = 1024 * 1024
	t.Cleanup(func() {
		common.APIRequestLogAsyncWrite = oldAsync
		common.APIRequestLogQueueSize = oldQueueSize
		common.APIRequestLogWorkers = oldWorkers
		common.APIRequestLogMaxItemBytes = oldMaxItemBytes
		common.APIRequestLogMaxQueueBytes = oldMaxQueueBytes
	})

	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		Source:      APIRequestLogSourceLive,
		UsageLogId:  11,
		Username:    "async-user",
		TokenName:   "async-token",
		ModelName:   "gpt-async",
		CreatedAt:   101,
		RequestId:   "req-async",
		ParseStatus: APIRequestLogParseOK,
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("async hello")},
		},
	}))

	var logs []APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.Find(&logs).Error)
	require.Len(t, logs, 1)
	require.Equal(t, APIRequestLogItemsPending, logs[0].ItemsStatus)

	var detail *APIRequestLog
	require.Eventually(t, func() bool {
		var err error
		detail, err = GetAPIRequestLogById(logs[0].Id)
		return err == nil && len(detail.Items) == 1 && detail.ItemsStatus == APIRequestLogItemsOK
	}, 2*time.Second, 20*time.Millisecond)
	require.Equal(t, APIRequestLogBody("async hello"), detail.Items[0].Content)

	var stored APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.First(&stored, logs[0].Id).Error)
	require.Equal(t, APIRequestLogItemsPending, stored.ItemsStatus)
}

func TestCreateAPIRequestLogAsyncItemsQueueByteLimit(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	droppedJobsBefore := atomic.LoadInt64(&apiRequestLogQueueDroppedJobs)
	droppedItemsBefore := atomic.LoadInt64(&apiRequestLogQueueDroppedItems)
	droppedBytesBefore := atomic.LoadInt64(&apiRequestLogQueueDroppedItemBytes)

	oldAsync := common.APIRequestLogAsyncWrite
	oldQueueSize := common.APIRequestLogQueueSize
	oldWorkers := common.APIRequestLogWorkers
	oldMaxItemBytes := common.APIRequestLogMaxItemBytes
	oldMaxQueueBytes := common.APIRequestLogMaxQueueBytes
	common.APIRequestLogAsyncWrite = true
	common.APIRequestLogQueueSize = 4
	common.APIRequestLogWorkers = 1
	common.APIRequestLogMaxItemBytes = 1024
	common.APIRequestLogMaxQueueBytes = 8
	t.Cleanup(func() {
		common.APIRequestLogAsyncWrite = oldAsync
		common.APIRequestLogQueueSize = oldQueueSize
		common.APIRequestLogWorkers = oldWorkers
		common.APIRequestLogMaxItemBytes = oldMaxItemBytes
		common.APIRequestLogMaxQueueBytes = oldMaxQueueBytes
	})

	err := CreateAPIRequestLog(&APIRequestLog{
		Source:      APIRequestLogSourceLive,
		UsageLogId:  13,
		Username:    "queue-user",
		ModelName:   "gpt-queue",
		CreatedAt:   103,
		RequestId:   "req-queue-limit",
		ParseStatus: APIRequestLogParseOK,
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("123456789abcdef")},
		},
	})
	require.Error(t, err)

	var log APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.First(&log, "usage_log_id = ?", 13).Error)
	require.Equal(t, APIRequestLogItemsFailed, log.ItemsStatus)
	require.Contains(t, log.ItemsError, "byte limit")

	var count int64
	require.NoError(t, REQUEST_LOG_DB.Model(&APIRequestLogItem{}).Where("log_id = ?", log.Id).Count(&count).Error)
	require.Equal(t, int64(0), count)
	require.Equal(t, droppedJobsBefore+1, atomic.LoadInt64(&apiRequestLogQueueDroppedJobs))
	require.Equal(t, droppedItemsBefore+1, atomic.LoadInt64(&apiRequestLogQueueDroppedItems))
	require.Greater(t, atomic.LoadInt64(&apiRequestLogQueueDroppedItemBytes), droppedBytesBefore)
}

func TestCreateAPIRequestLogTruncatesLargeItems(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	oldMaxItemBytes := common.APIRequestLogMaxItemBytes
	common.APIRequestLogMaxItemBytes = 8
	t.Cleanup(func() {
		common.APIRequestLogMaxItemBytes = oldMaxItemBytes
	})

	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		UsageLogId:  12,
		Username:    "truncate-user",
		ModelName:   "gpt-truncate",
		CreatedAt:   102,
		ParseStatus: APIRequestLogParseOK,
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("123456789abcdef")},
		},
	}))

	var log APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.Preload("Items").First(&log, "usage_log_id = ?", 12).Error)
	require.Len(t, log.Items, 1)
	require.True(t, log.Items[0].Truncated)
	require.Contains(t, string(log.Items[0].Content), "[TRUNCATED]")
}

func TestCreateAPIRequestLogUsageOnlyUpdatePreservesItems(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		UsageLogId:  10,
		Username:    "alice",
		TokenName:   "prod-token",
		ModelName:   "gpt-test",
		Quota:       1,
		TokenUsed:   2,
		ParseStatus: APIRequestLogParseOK,
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("hello")},
			{Seq: 2, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: APIRequestLogBody("ok")},
		},
	}))

	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		UsageLogId: 10,
		Username:   "alice-renamed",
		Quota:      10,
		TokenUsed:  20,
	}))

	var logs []APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.Find(&logs).Error)
	require.Len(t, logs, 1)
	require.Equal(t, "alice-renamed", logs[0].Username)
	require.Equal(t, 10, logs[0].Quota)

	detail, err := GetAPIRequestLogById(logs[0].Id)
	require.NoError(t, err)
	require.Len(t, detail.Items, 2)
	require.Equal(t, APIRequestLogBody("hello"), detail.Items[0].Content)
	require.Equal(t, APIRequestLogBody("ok"), detail.Items[1].Content)
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
	require.JSONEq(t, `{"foo":"bar"}`, consumeLog.Other)

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
	require.Positive(t, requestLog.RequestSize)
	require.Empty(t, requestLog.RequestBody)
	require.Empty(t, requestLog.ResponseBody)
	require.Equal(t, consumeLog.Quota, requestLog.Quota)
	require.Equal(t, consumeLog.PromptTokens+consumeLog.CompletionTokens, requestLog.TokenUsed)

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
		ResponseOmittedReason: "capture_disabled",
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("hello")},
		},
	}))

	var count int64
	require.NoError(t, LOG_DB.Model(&APIRequestLog{}).Count(&count).Error)
	require.Equal(t, int64(1), count)

	detail, err := GetAPIRequestLogById(requestLog.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, detail.StatusCode)
	require.Empty(t, detail.RequestBody)
	require.Len(t, detail.Items, 1)
	require.NotNil(t, detail.Usage)
	require.Equal(t, 123, detail.Usage.Quota)
	require.Equal(t, 18, detail.Usage.TokenUsed)
}

func TestExcludedCallLogUsernameSkipsNewLogs(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	oldEnabled := common.APIRequestLogEnabled
	oldLogConsumeEnabled := common.LogConsumeEnabled
	common.APIRequestLogEnabled = true
	common.LogConsumeEnabled = true
	common.SetCallLogExcludedUsernames("ryan")
	t.Cleanup(func() {
		common.APIRequestLogEnabled = oldEnabled
		common.LogConsumeEnabled = oldLogConsumeEnabled
		common.SetCallLogExcludedUsernames("ryan")
	})

	require.NoError(t, DB.Create(&User{
		Id:       77,
		Username: "ryan",
		Status:   common.UserStatusEnabled,
		Group:    "default",
	}).Error)

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"messages":[]}`))
	ctx.Set("username", "Ryan")

	require.Nil(t, RecordConsumeLog(ctx, 77, RecordConsumeLogParams{
		ModelName: "gpt-excluded",
		Quota:     10,
	}))
	RecordErrorLog(ctx, 77, 1, "gpt-excluded", "token", "error", 1, 1, false, "default", map[string]interface{}{"status": 500})
	RecordLog(77, LogTypeConsume, "generic consume")
	RecordLogWithAdminInfo(77, LogTypeConsume, "generic consume", map[string]interface{}{"admin": true})
	require.Nil(t, RecordTaskBillingLog(RecordTaskBillingLogParams{
		UserId:    77,
		LogType:   LogTypeConsume,
		ModelName: "task-excluded",
		Quota:     10,
	}))
	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		Username:  "ryan",
		ModelName: "gpt-excluded",
		CreatedAt: 100,
		RequestId: "req-excluded",
		Items: []APIRequestLogItem{{
			Seq:         1,
			Phase:       APIRequestLogPhaseInput,
			ItemType:    APIRequestLogItemMessage,
			Role:        "user",
			ContentType: "text",
			Content:     APIRequestLogBody("private"),
		}},
	}))

	var logCount int64
	require.NoError(t, LOG_DB.Model(&Log{}).Count(&logCount).Error)
	require.Zero(t, logCount)
	var requestLogCount int64
	require.NoError(t, REQUEST_LOG_DB.Model(&APIRequestLog{}).Count(&requestLogCount).Error)
	require.Zero(t, requestLogCount)
}

func TestGetAPIRequestLogsReadsRequestLogs(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		UsageLogId:       55,
		UserId:           9,
		Username:         "backfill-user",
		ModelName:        "gpt-backfill",
		TokenName:        "backfill-token",
		TokenId:          12,
		ChannelId:        34,
		Group:            "default",
		Quota:            456,
		PromptTokens:     20,
		CompletionTokens: 30,
		TokenUsed:        50,
		CreatedAt:        200,
		RequestId:        "req-backfill",
	}))

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
	require.Equal(t, 55, items[0].UsageLogId)
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
	oldRequestLogDB := REQUEST_LOG_DB
	LOG_DB = db
	REQUEST_LOG_DB = db
	t.Cleanup(func() {
		LOG_DB = oldLogDB
		REQUEST_LOG_DB = oldRequestLogDB
	})

	require.False(t, REQUEST_LOG_DB.Migrator().HasTable(&APIRequestLog{}))
	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		Username:  "alice",
		TokenName: "prod-token",
		ModelName: "gpt-test",
		CreatedAt: 100,
	}))
	require.True(t, REQUEST_LOG_DB.Migrator().HasTable(&APIRequestLog{}))
	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		CreatedAt: 101,
		RequestId: "req-status",
	}))

	status, err := GetAPIRequestLogStorageStatus()
	require.NoError(t, err)
	require.True(t, status.HasTable)
	require.Equal(t, int64(2), status.Count)
	require.Equal(t, "req-status", status.LastRequestId)
}

func TestInitRequestLogDBRequiresDSNWhenEnabled(t *testing.T) {
	oldEnabled := common.APIRequestLogEnabled
	oldRequestLogDB := REQUEST_LOG_DB
	common.APIRequestLogEnabled = true
	REQUEST_LOG_DB = nil
	t.Setenv("REQUEST_LOG_SQL_DSN", "")
	t.Cleanup(func() {
		common.APIRequestLogEnabled = oldEnabled
		REQUEST_LOG_DB = oldRequestLogDB
	})

	require.Error(t, InitRequestLogDB())
	require.Nil(t, REQUEST_LOG_DB)
}
