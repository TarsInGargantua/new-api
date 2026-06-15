package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetAllQuotaDatesFiltersByUsernameAndModel(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	require.NoError(t, DB.Create(&QuotaData{
		UserID:    1,
		Username:  "alice",
		ModelName: "gpt-5",
		CreatedAt: 100,
		Count:     1,
		Quota:     10,
		TokenUsed: 2,
	}).Error)
	require.NoError(t, DB.Create(&QuotaData{
		UserID:    1,
		Username:  "alice",
		ModelName: "gpt-4",
		CreatedAt: 200,
		Count:     1,
		Quota:     20,
		TokenUsed: 3,
	}).Error)
	require.NoError(t, DB.Create(&QuotaData{
		UserID:    2,
		Username:  "bob",
		ModelName: "gpt-5",
		CreatedAt: 100,
		Count:     2,
		Quota:     30,
		TokenUsed: 4,
	}).Error)
	require.NoError(t, DB.Create(&QuotaData{
		UserID:    3,
		Username:  "alice2",
		ModelName: "gpt-5",
		CreatedAt: 100,
		Count:     4,
		Quota:     50,
		TokenUsed: 6,
	}).Error)

	rows, err := GetAllQuotaDates(0, 0, "alice", "gpt-5")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "alice", rows[0].Username)
	require.Equal(t, "gpt-5", rows[0].ModelName)
	require.Equal(t, 10, rows[0].Quota)

	rows, err = GetAllQuotaDates(0, 0, "", "gpt-5")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "gpt-5", rows[0].ModelName)
	require.Equal(t, int64(100), rows[0].CreatedAt)
	require.Equal(t, 90, rows[0].Quota)
	require.Equal(t, 7, rows[0].Count)
	require.Equal(t, 12, rows[0].TokenUsed)
}

func TestGetUserDailyUsageStatsFiltersByUsernameAndModel(t *testing.T) {
	setupAPIRequestLogTestDB(t)

	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           1,
		Username:         "alice",
		Type:             LogTypeConsume,
		ModelName:        "gpt-5",
		Quota:            10,
		PromptTokens:     2,
		CompletionTokens: 3,
		CreatedAt:        86400,
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           1,
		Username:         "alice",
		Type:             LogTypeConsume,
		ModelName:        "gpt-4",
		Quota:            20,
		PromptTokens:     4,
		CompletionTokens: 5,
		CreatedAt:        86400,
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           2,
		Username:         "bob",
		Type:             LogTypeConsume,
		ModelName:        "gpt-5",
		Quota:            30,
		PromptTokens:     6,
		CompletionTokens: 7,
		CreatedAt:        86400,
	}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		UserId:           3,
		Username:         "alice2",
		Type:             LogTypeConsume,
		ModelName:        "gpt-5",
		Quota:            40,
		PromptTokens:     8,
		CompletionTokens: 9,
		CreatedAt:        86400,
	}).Error)

	rows, err := GetUserDailyUsageStats(0, 0, "gpt-5", "alice")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[0].UserId)
	require.Equal(t, "alice", rows[0].Username)
	require.Equal(t, int64(10), rows[0].Quota)
	require.Equal(t, int64(5), rows[0].TokenUsed)
	require.Equal(t, int64(1), rows[0].Count)
}
