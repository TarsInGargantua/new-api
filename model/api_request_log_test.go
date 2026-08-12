package model

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAPIRequestLogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	resetAPIRequestLogItemQueueForTest()
	resetAPIRequestLogOutboxWorkersForTest()
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
		resetAPIRequestLogOutboxWorkersForTest()
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

func TestRequestLogConnectionPoolConfigUsesDedicatedSafeDefaults(t *testing.T) {
	t.Setenv("REQUEST_LOG_SQL_MAX_IDLE_CONNS", "")
	t.Setenv("REQUEST_LOG_SQL_MAX_OPEN_CONNS", "")
	t.Setenv("REQUEST_LOG_SQL_MAX_LIFETIME", "")
	t.Setenv("SQL_MAX_IDLE_CONNS", "100")
	t.Setenv("SQL_MAX_OPEN_CONNS", "1000")
	t.Setenv("SQL_MAX_LIFETIME", "60")

	config := requestLogConnectionPoolConfigFromEnv()
	require.Equal(t, requestLogDefaultMaxIdle, config.MaxIdleConns)
	require.Equal(t, requestLogDefaultMaxOpen, config.MaxOpenConns)
	require.Equal(t, time.Duration(requestLogDefaultLifetime)*time.Second, config.MaxLifetime)
}

func TestRequestLogOutboxPersistsLocallyThenSynchronizesToDedicatedDatabase(t *testing.T) {
	localDB := setupAPIRequestLogTestDB(t)
	remoteDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "request-log-remote.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, remoteDB.AutoMigrate(&APIRequestLog{}, &APIRequestLogItem{}))
	require.NoError(t, EnsureAPIRequestLogMaterializedTables(remoteDB))

	oldEnabled := common.APIRequestLogEnabled
	oldOutboxEnabled := common.APIRequestLogOutboxEnabled
	oldDeferred := common.APIRequestLogDeferredMaterialization
	oldRequestLogDB := REQUEST_LOG_DB
	common.APIRequestLogEnabled = true
	common.APIRequestLogOutboxEnabled = true
	common.APIRequestLogDeferredMaterialization = false
	REQUEST_LOG_DB = remoteDB
	t.Cleanup(func() {
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogOutboxEnabled = oldOutboxEnabled
		common.APIRequestLogDeferredMaterialization = oldDeferred
		REQUEST_LOG_DB = oldRequestLogDB
	})

	log := &APIRequestLog{
		Source:     APIRequestLogSourceLive,
		UsageLogId: 931,
		RequestId:  "request-outbox-931",
		CreatedAt:  1780000000,
		ModelName:  "gpt-outbox",
		Items: []APIRequestLogItem{{
			Seq:      1,
			Phase:    APIRequestLogPhaseInput,
			ItemType: APIRequestLogItemMessage,
			Content:  "hello from durable outbox",
		}},
	}
	require.NoError(t, CreateAPIRequestLog(log))

	var queued int64
	require.NoError(t, localDB.Model(&APIRequestLogOutbox{}).Count(&queued).Error)
	require.EqualValues(t, 1, queued)
	var remoteCount int64
	require.NoError(t, remoteDB.Model(&APIRequestLog{}).Count(&remoteCount).Error)
	require.Zero(t, remoteCount, "HTTP path must not write the remote request-log database")

	require.NoError(t, syncAPIRequestLogOutboxBatch())
	require.NoError(t, localDB.Model(&APIRequestLogOutbox{}).Count(&queued).Error)
	require.Zero(t, queued)

	var persisted APIRequestLog
	require.NoError(t, remoteDB.Preload("Items").Where("usage_log_id = ?", 931).First(&persisted).Error)
	require.Equal(t, "request-outbox-931", persisted.RequestId)
	require.Len(t, persisted.Items, 1)
	require.Equal(t, "hello from durable outbox", string(persisted.Items[0].Content))
}

func TestRequestLogOutboxKeepsFailedRemoteWriteForRetry(t *testing.T) {
	localDB := setupAPIRequestLogTestDB(t)
	remoteDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "request-log-remote-retry.db")), &gorm.Config{})
	require.NoError(t, err)
	// Deliberately omit the materialized tables. A log with items will fail
	// after the parent/items are written, which exercises the retry path.
	require.NoError(t, remoteDB.AutoMigrate(&APIRequestLog{}, &APIRequestLogItem{}))

	oldEnabled := common.APIRequestLogEnabled
	oldOutboxEnabled := common.APIRequestLogOutboxEnabled
	oldDeferred := common.APIRequestLogDeferredMaterialization
	oldRequestLogDB := REQUEST_LOG_DB
	common.APIRequestLogEnabled = true
	common.APIRequestLogOutboxEnabled = true
	common.APIRequestLogDeferredMaterialization = false
	REQUEST_LOG_DB = remoteDB
	t.Cleanup(func() {
		common.APIRequestLogEnabled = oldEnabled
		common.APIRequestLogOutboxEnabled = oldOutboxEnabled
		common.APIRequestLogDeferredMaterialization = oldDeferred
		REQUEST_LOG_DB = oldRequestLogDB
	})

	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		Source:     APIRequestLogSourceLive,
		UsageLogId: 932,
		RequestId:  "request-outbox-retry-932",
		CreatedAt:  1780000001,
		Items:      []APIRequestLogItem{{Seq: 1, ItemType: APIRequestLogItemMessage, Content: "retry me"}},
	}))
	err = syncAPIRequestLogOutboxBatch()
	require.Error(t, err)

	var failed APIRequestLogOutbox
	require.NoError(t, localDB.First(&failed).Error)
	require.NotEmpty(t, failed.LastError)
	require.GreaterOrEqual(t, failed.Attempts, 1)

	require.NoError(t, EnsureAPIRequestLogMaterializedTables(remoteDB))
	require.NoError(t, localDB.Model(&APIRequestLogOutbox{}).Where("id = ?", failed.Id).Update("available_at", common.GetTimestamp()).Error)
	require.NoError(t, syncAPIRequestLogOutboxBatch())

	var remaining int64
	require.NoError(t, localDB.Model(&APIRequestLogOutbox{}).Count(&remaining).Error)
	require.Zero(t, remaining)
}

func TestRequestLogConnectionPoolConfigClampsInvalidValues(t *testing.T) {
	t.Setenv("REQUEST_LOG_SQL_MAX_IDLE_CONNS", "99")
	t.Setenv("REQUEST_LOG_SQL_MAX_OPEN_CONNS", "12")
	t.Setenv("REQUEST_LOG_SQL_MAX_LIFETIME", "600")

	config := requestLogConnectionPoolConfigFromEnv()
	require.Equal(t, 12, config.MaxIdleConns)
	require.Equal(t, 12, config.MaxOpenConns)
	require.Equal(t, 600*time.Second, config.MaxLifetime)

	if strconv.IntSize == 64 {
		t.Setenv("REQUEST_LOG_SQL_MAX_LIFETIME", "9223372036854775807")
		config = requestLogConnectionPoolConfigFromEnv()
		require.Equal(t, requestLogMaxLifetime, config.MaxLifetime)
	}

	t.Setenv("REQUEST_LOG_SQL_MAX_LIFETIME", "-1")
	config = requestLogConnectionPoolConfigFromEnv()
	require.Zero(t, config.MaxLifetime)

	t.Setenv("REQUEST_LOG_SQL_MAX_IDLE_CONNS", "-1")
	t.Setenv("REQUEST_LOG_SQL_MAX_OPEN_CONNS", "0")
	config = requestLogConnectionPoolConfigFromEnv()
	require.Zero(t, config.MaxIdleConns)
	require.Equal(t, 1, config.MaxOpenConns)
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

func TestCreateAPIRequestLogLiveItemsPersistSynchronouslyWithoutVolatileOptIn(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	oldAsync := common.APIRequestLogAsyncWrite
	t.Setenv("API_REQUEST_LOG_ASYNC_WRITE", "true")
	t.Setenv("API_REQUEST_LOG_ALLOW_VOLATILE_ASYNC_WRITE", "false")
	common.APIRequestLogAsyncWrite = common.APIRequestLogAsyncWriteEnabledFromEnv()
	t.Cleanup(func() {
		common.APIRequestLogAsyncWrite = oldAsync
	})
	require.False(t, common.APIRequestLogAsyncWrite)

	log := &APIRequestLog{
		Source:      APIRequestLogSourceLive,
		UsageLogId:  11,
		Username:    "sync-user",
		TokenName:   "sync-token",
		ModelName:   "gpt-sync",
		CreatedAt:   101,
		RequestId:   "req-sync",
		ParseStatus: APIRequestLogParseOK,
		TurnMeta: &APIRequestLogTurnMeta{
			SessionId:        "sync-session",
			TurnId:           "sync-turn",
			Protocol:         "codex",
			StartedAt:        101,
			CompletedAt:      102,
			CompletionStatus: APIRequestLogTurnStatusCompleted,
			CompletionSignal: "message.final",
			Attribution:      APIRequestLogTurnAttributionExact,
			Items: []APIRequestLogTurnItemMeta{
				{Seq: 1, ProviderItemId: "sync-item", TurnId: "sync-turn", MessagePhase: "final_answer", ItemStatus: "completed"},
			},
		},
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("sync hello")},
		},
	}

	require.NoError(t, CreateAPIRequestLog(log))
	require.Equal(t, APIRequestLogItemsOK, log.ItemsStatus)

	var stored APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.Preload("Items").First(&stored, "usage_log_id = ?", 11).Error)
	require.Equal(t, APIRequestLogItemsOK, stored.ItemsStatus)
	require.Empty(t, stored.ItemsError)
	require.Len(t, stored.Items, 1)
	require.Equal(t, APIRequestLogBody("sync hello"), stored.Items[0].Content)

	var turnRequestCount int64
	require.NoError(t, REQUEST_LOG_DB.Model(&APIRequestLogTurnRequest{}).Where("log_id = ?", stored.Id).Count(&turnRequestCount).Error)
	require.Equal(t, int64(1), turnRequestCount)
	var turn APIRequestLogTurn
	require.NoError(t, REQUEST_LOG_DB.Joins("JOIN api_request_log_turn_requests request ON request.turn_record_id = api_request_log_turns.id").Where("request.log_id = ?", stored.Id).First(&turn).Error)
	require.Equal(t, "sync-session", turn.SessionId)
	require.Equal(t, "sync-turn", turn.TurnId)
	require.Equal(t, APIRequestLogTurnAttributionExact, turn.Attribution)
	require.Equal(t, APIRequestLogTurnStatusCompleted, turn.CompletionStatus)
	var turnItemCount int64
	require.NoError(t, REQUEST_LOG_DB.Model(&APIRequestLogTurnItem{}).Where("source_item_id = ?", stored.Items[0].Id).Count(&turnItemCount).Error)
	require.Equal(t, int64(1), turnItemCount)

	status, err := GetAPIRequestLogStorageStatus()
	require.NoError(t, err)
	require.False(t, status.AsyncWrite)
	require.Zero(t, status.QueueDepth)
	require.Zero(t, status.QueuedItemBytes)
}

func TestCreateAPIRequestLogVolatileAsyncOptInItems(t *testing.T) {
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

	var stored APIRequestLog
	require.Eventually(t, func() bool {
		stored = APIRequestLog{}
		err := REQUEST_LOG_DB.Preload("Items").First(&stored, logs[0].Id).Error
		return err == nil && stored.ItemsStatus == APIRequestLogItemsOK && len(stored.Items) == 1
	}, 2*time.Second, 20*time.Millisecond)
	require.Empty(t, stored.ItemsError)
	require.Equal(t, APIRequestLogBody("async hello"), stored.Items[0].Content)
}

func TestCreateAPIRequestLogVolatileAsyncOptInQueueByteLimit(t *testing.T) {
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

func TestCreateAPIRequestLogVolatileAsyncOptInMissingQueueMarksFailed(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	oldAsync := common.APIRequestLogAsyncWrite
	common.APIRequestLogAsyncWrite = true
	t.Setenv("REQUEST_LOG_DB_READ_ONLY", "true")
	t.Cleanup(func() {
		common.APIRequestLogAsyncWrite = oldAsync
	})

	err := CreateAPIRequestLog(&APIRequestLog{
		Source:      APIRequestLogSourceLive,
		UsageLogId:  14,
		Username:    "missing-queue-user",
		ModelName:   "gpt-missing-queue",
		CreatedAt:   104,
		RequestId:   "req-missing-queue",
		ParseStatus: APIRequestLogParseOK,
		TurnMeta: &APIRequestLogTurnMeta{
			SessionId:        "missing-queue-session",
			TurnId:           "missing-queue-turn",
			Protocol:         "codex",
			StartedAt:        104,
			CompletedAt:      105,
			CompletionStatus: APIRequestLogTurnStatusCompleted,
			CompletionSignal: "message.final",
			Attribution:      APIRequestLogTurnAttributionExact,
		},
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("not queued")},
		},
	})
	require.ErrorContains(t, err, "queue is not initialized")

	var stored APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.First(&stored, "usage_log_id = ?", 14).Error)
	require.Equal(t, APIRequestLogItemsFailed, stored.ItemsStatus)
	require.Contains(t, stored.ItemsError, "queue is not initialized")

	var turn APIRequestLogTurn
	require.NoError(t, REQUEST_LOG_DB.Joins("JOIN api_request_log_turn_requests request ON request.turn_record_id = api_request_log_turns.id").Where("request.log_id = ?", stored.Id).First(&turn).Error)
	require.Equal(t, APIRequestLogTurnStatusOpen, turn.CompletionStatus)
}

func TestCreateAPIRequestLogItemsIfMissingRepairsPartialWrite(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	parent := &APIRequestLog{
		Source:      APIRequestLogSourceLive,
		UsageLogId:  17,
		Username:    "partial-item-user",
		ModelName:   "gpt-partial-item",
		CreatedAt:   107,
		ParseStatus: APIRequestLogParseOK,
		ItemsStatus: APIRequestLogItemsPending,
	}
	require.NoError(t, REQUEST_LOG_DB.Create(parent).Error)
	expected := []APIRequestLogItem{
		{LogId: parent.Id, Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("first")},
		{LogId: parent.Id, Seq: 2, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: APIRequestLogBody("second")},
	}
	require.NoError(t, REQUEST_LOG_DB.Create(&expected[0]).Error)
	partial, err := GetAPIRequestLogById(parent.Id)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogItemsPending, partial.ItemsStatus)
	require.Len(t, partial.Items, 1)

	require.NoError(t, createAPIRequestLogItemsIfMissing(REQUEST_LOG_DB, parent.Id, expected))
	require.NoError(t, updateAPIRequestLogItemsStatus(REQUEST_LOG_DB, parent.Id, APIRequestLogItemsOK, ""))

	var stored []APIRequestLogItem
	require.NoError(t, REQUEST_LOG_DB.Where("log_id = ?", parent.Id).Order("seq asc").Find(&stored).Error)
	require.Len(t, stored, 2)
	require.Equal(t, []int{1, 2}, []int{stored[0].Seq, stored[1].Seq})
	complete, err := GetAPIRequestLogById(parent.Id)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogItemsOK, complete.ItemsStatus)
	require.Len(t, complete.Items, 2)
}

func TestCreateAPIRequestLogItemsIfMissingRecomputesAfterPartialBatchFailure(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	parent := &APIRequestLog{Source: APIRequestLogSourceLive, UsageLogId: 18, CreatedAt: 108, ItemsStatus: APIRequestLogItemsPending}
	require.NoError(t, REQUEST_LOG_DB.Create(parent).Error)
	expected := []APIRequestLogItem{
		{LogId: parent.Id, Seq: 1, ItemType: APIRequestLogItemMessage, Content: APIRequestLogBody("first")},
		{LogId: parent.Id, Seq: 2, ItemType: APIRequestLogItemMessage, Content: APIRequestLogBody("second")},
	}
	var calls [][]int
	writer := func(db *gorm.DB, items []APIRequestLogItem) error {
		seqs := make([]int, len(items))
		for i := range items {
			seqs[i] = items[i].Seq
		}
		calls = append(calls, seqs)
		if len(calls) == 1 {
			require.NoError(t, db.Create(&items[0]).Error)
			return errors.New("simulated deadlock after partial batch")
		}
		return db.CreateInBatches(items, len(items)).Error
	}

	require.NoError(t, createAPIRequestLogItemsIfMissingWithWriter(REQUEST_LOG_DB, parent.Id, expected, writer))
	require.Equal(t, [][]int{{1, 2}, {2}}, calls)
}

func TestSplitAPIRequestLogItemBatchesHonorsItemAndByteLimits(t *testing.T) {
	items := []APIRequestLogItem{
		{Seq: 1, Content: APIRequestLogBody("12345")},
		{Seq: 2, Content: APIRequestLogBody("12345")},
		{Seq: 3, Content: APIRequestLogBody("12345")},
		{Seq: 4, Content: APIRequestLogBody("12345")},
		{Seq: 5, Content: APIRequestLogBody("12345")},
	}

	byCount := splitAPIRequestLogItemBatches(items, 2, 1024)
	require.Len(t, byCount, 3)
	require.Len(t, byCount[0], 2)
	require.Len(t, byCount[1], 2)
	require.Len(t, byCount[2], 1)

	byBytes := splitAPIRequestLogItemBatches(items, 100, 9)
	require.Len(t, byBytes, 5)
	for _, batch := range byBytes {
		require.Len(t, batch, 1)
	}
}

func TestCreateAPIRequestLogItemsIfMissingRemovesUnreferencedDuplicates(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	require.NoError(t, EnsureAPIRequestLogMaterializedTables(REQUEST_LOG_DB))

	parent := &APIRequestLog{Source: APIRequestLogSourceLive, UsageLogId: 19, CreatedAt: 109, ItemsStatus: APIRequestLogItemsFailed}
	require.NoError(t, REQUEST_LOG_DB.Create(parent).Error)
	expected := []APIRequestLogItem{
		{LogId: parent.Id, Seq: 1, ItemType: APIRequestLogItemMessage, Content: APIRequestLogBody("first")},
		{LogId: parent.Id, Seq: 2, ItemType: APIRequestLogItemMessage, Content: APIRequestLogBody("second")},
	}
	duplicated := []APIRequestLogItem{expected[0], expected[1], expected[0], expected[1]}
	require.NoError(t, REQUEST_LOG_DB.Create(&duplicated).Error)

	require.NoError(t, createAPIRequestLogItemsIfMissing(REQUEST_LOG_DB, parent.Id, expected))

	var stored []APIRequestLogItem
	require.NoError(t, REQUEST_LOG_DB.Where("log_id = ?", parent.Id).Order("seq ASC").Find(&stored).Error)
	require.Len(t, stored, 2)
	require.Equal(t, []int{1, 2}, []int{stored[0].Seq, stored[1].Seq})
}

func TestCreateAPIRequestLogItemsIfMissingPreservesReferencedDuplicate(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	require.NoError(t, EnsureAPIRequestLogMaterializedTables(REQUEST_LOG_DB))

	parent := &APIRequestLog{Source: APIRequestLogSourceLive, UsageLogId: 20, CreatedAt: 110, ItemsStatus: APIRequestLogItemsFailed}
	require.NoError(t, REQUEST_LOG_DB.Create(parent).Error)
	expected := []APIRequestLogItem{{LogId: parent.Id, Seq: 1, ItemType: APIRequestLogItemMessage, Content: APIRequestLogBody("first")}}
	duplicated := []APIRequestLogItem{expected[0], expected[0]}
	require.NoError(t, REQUEST_LOG_DB.Create(&duplicated).Error)
	turn := APIRequestLogTurn{OwnerFingerprint: "owner", SessionId: "session", TurnId: "turn", TurnIndex: 1}
	require.NoError(t, REQUEST_LOG_DB.Create(&turn).Error)
	request := APIRequestLogTurnRequest{TurnRecordId: turn.Id, LogId: parent.Id, Sequence: 1}
	require.NoError(t, REQUEST_LOG_DB.Create(&request).Error)
	require.NoError(t, REQUEST_LOG_DB.Create(&APIRequestLogTurnItem{
		TurnRecordId: turn.Id, RequestRecordId: request.Id, SourceItemId: duplicated[1].Id, Ordinal: 1, CanonicalKey: "referenced-surplus",
	}).Error)

	require.NoError(t, createAPIRequestLogItemsIfMissing(REQUEST_LOG_DB, parent.Id, expected))

	var count int64
	require.NoError(t, REQUEST_LOG_DB.Model(&APIRequestLogItem{}).Where("log_id = ?", parent.Id).Count(&count).Error)
	require.Equal(t, int64(1), count)
	var mapping APIRequestLogTurnItem
	require.NoError(t, REQUEST_LOG_DB.First(&mapping).Error)
	require.Equal(t, duplicated[1].Id, mapping.SourceItemId)
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

func TestCreateAPIRequestLogSanitizesInvalidUTF8BelowItemLimit(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	oldMaxItemBytes := common.APIRequestLogMaxItemBytes
	common.APIRequestLogMaxItemBytes = 1024
	t.Cleanup(func() {
		common.APIRequestLogMaxItemBytes = oldMaxItemBytes
	})

	invalidContent := APIRequestLogBody(string([]byte{'o', 'k', 0xff, 'x'}))
	require.False(t, utf8.ValidString(string(invalidContent)))
	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		UsageLogId:  15,
		Username:    "utf8-user",
		ModelName:   "gpt-utf8",
		CreatedAt:   105,
		ParseStatus: APIRequestLogParseOK,
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: invalidContent},
		},
	}))

	var stored APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.Preload("Items").First(&stored, "usage_log_id = ?", 15).Error)
	require.Len(t, stored.Items, 1)
	require.True(t, utf8.ValidString(string(stored.Items[0].Content)))
	require.Equal(t, "ok\ufffdx", string(stored.Items[0].Content))
}

func TestCreateAPIRequestLogNormalizedEmptyItemsSetEmptyStatus(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	log := &APIRequestLog{
		UsageLogId:  16,
		Username:    "empty-item-user",
		ModelName:   "gpt-empty-item",
		CreatedAt:   106,
		ParseStatus: APIRequestLogParseOK,
		Items: []APIRequestLogItem{
			{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody(" \n\t ")},
		},
	}
	require.NoError(t, CreateAPIRequestLog(log))
	require.Equal(t, APIRequestLogItemsEmpty, log.ItemsStatus)

	var stored APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.Preload("Items").First(&stored, "usage_log_id = ?", 16).Error)
	require.Equal(t, APIRequestLogItemsEmpty, stored.ItemsStatus)
	require.Empty(t, stored.ItemsError)
	require.Empty(t, stored.Items)
}

func TestCreateAPIRequestLogMaterializeFailureMarksItemsFailed(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	log := &APIRequestLog{
		Source: APIRequestLogSourceLive, UsageLogId: 19, Username: "turn-failure-user", ModelName: "gpt-turn-failure",
		CreatedAt: 109, ParseStatus: APIRequestLogParseOK,
		Items: []APIRequestLogItem{{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("persist me")}},
	}
	normalizeAPIRequestLog(log)
	materializeErr := errors.New("forced turn materialization failure")
	err := createOrUpdateAPIRequestLogWithMaterializer(log, func(*gorm.DB, *APIRequestLog, []APIRequestLogItem) error {
		return materializeErr
	})
	require.ErrorContains(t, err, materializeErr.Error())

	var stored APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.Preload("Items").First(&stored, log.Id).Error)
	require.Equal(t, APIRequestLogItemsFailed, stored.ItemsStatus)
	require.Contains(t, stored.ItemsError, "materialize request log turn")
	require.Len(t, stored.Items, 1)
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
	require.Equal(t, APIRequestLogItemsOK, logs[0].ItemsStatus)

	detail, err := GetAPIRequestLogById(logs[0].Id)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogItemsOK, detail.ItemsStatus)
	require.Len(t, detail.Items, 2)
	require.Equal(t, APIRequestLogBody("hello"), detail.Items[0].Content)
	require.Equal(t, APIRequestLogBody("ok"), detail.Items[1].Content)
}

func TestCreateAPIRequestLogUsageOnlyStatusDefaultsAndExplicitValue(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{UsageLogId: 20, Username: "empty-status", CreatedAt: 110}))
	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{
		UsageLogId: 21, Username: "explicit-status", CreatedAt: 111,
		ItemsStatus: APIRequestLogItemsFailed, ItemsError: "explicit failure",
	}))

	var emptyLog APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.First(&emptyLog, "usage_log_id = ?", 20).Error)
	require.Equal(t, APIRequestLogItemsEmpty, emptyLog.ItemsStatus)
	var explicitLog APIRequestLog
	require.NoError(t, REQUEST_LOG_DB.First(&explicitLog, "usage_log_id = ?", 21).Error)
	require.Equal(t, APIRequestLogItemsFailed, explicitLog.ItemsStatus)
	require.Equal(t, "explicit failure", explicitLog.ItemsError)
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
