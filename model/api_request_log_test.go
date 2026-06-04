package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAPIRequestLogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&APIRequestLog{}))

	oldLogDB := LOG_DB
	oldLogGroupCol := logGroupCol
	LOG_DB = db
	logGroupCol = "`group`"
	t.Cleanup(func() {
		LOG_DB = oldLogDB
		logGroupCol = oldLogGroupCol
	})
	return db
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

	detail, err := GetAPIRequestLogById(items[0].Id)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogBody(`{"prompt":"hello"}`), detail.RequestBody)
	require.Equal(t, APIRequestLogBody(`data: {"ok":true}`), detail.ResponseBody)
}
