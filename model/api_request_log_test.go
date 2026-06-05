package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

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

	err := CreateAPIRequestLog(&APIRequestLog{
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
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           2,
		Username:         "alice",
		Type:             LogTypeConsume,
		ModelName:        "gpt-test",
		TokenName:        "prod-token",
		Quota:            1234,
		PromptTokens:     100,
		CompletionTokens: 20,
		UseTime:          3,
		RequestId:        "req-1",
		Content:          "consume detail",
		Other:            `{"foo":"bar"}`,
	}).Error)
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
