package model

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupAPIRequestLogTurnTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "turn-test.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&APIRequestLog{}, &APIRequestLogItem{}))
	require.NoError(t, EnsureAPIRequestLogMaterializedTables(db))
	return db
}

func createAPIRequestLogTurnTestRequest(t *testing.T, db *gorm.DB, log APIRequestLog, items []APIRequestLogItem) (*APIRequestLog, []APIRequestLogItem) {
	t.Helper()
	log.Items = nil
	require.NoError(t, db.Create(&log).Error)
	for index := range items {
		items[index].LogId = log.Id
	}
	if len(items) > 0 {
		require.NoError(t, db.Create(&items).Error)
	}
	return &log, items
}

func TestMaterializeAPIRequestLogTurnsMergesRequestsAndKeepsCurrentTurnOnly(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)

	firstLog, firstItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 2, TokenName: "prod", ModelName: "gpt-turn", CreatedAt: 100,
		RequestId: "request-1", StatusCode: 200, IsStream: true, PromptTokens: 10, CompletionTokens: 2, TokenUsed: 12, Quota: 20,
	}, []APIRequestLogItem{
		{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "system", ContentType: "text", Content: "system instructions"},
		{Seq: 2, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "developer", ContentType: "text", Content: "developer instructions"},
		{Seq: 3, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemToolSpec, Name: "lookup", ContentType: "json", Content: `{"name":"lookup"}`},
		{Seq: 4, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "first question"},
		{Seq: 5, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "first answer"},
		{Seq: 6, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemReasoning, Role: "assistant", ContentType: "encrypted", Content: "ciphertext"},
	})
	turn, err := MaterializeAPIRequestLogTurn(db, firstLog, APIRequestLogTurnMeta{
		SessionId: "session-1", TurnId: "turn-1", Protocol: "codex", StartedAt: 100, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionExact,
		Items: []APIRequestLogTurnItemMeta{{Seq: 5, ProviderItemId: "assistant-1", TurnId: "turn-1", MessagePhase: "commentary", ItemStatus: "completed"}},
	}, firstItems)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogTurnStatusOpen, turn.CompletionStatus)
	require.Equal(t, 5, turn.ItemCount)

	secondLog, secondItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 2, TokenName: "prod", ModelName: "gpt-turn", CreatedAt: 110,
		RequestId: "request-2", StatusCode: 200, IsStream: true, PromptTokens: 12, CompletionTokens: 3, TokenUsed: 15, Quota: 30,
	}, []APIRequestLogItem{
		{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "system", ContentType: "text", Content: "system instructions"},
		{Seq: 2, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "developer", ContentType: "text", Content: "developer instructions"},
		{Seq: 3, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemToolSpec, Name: "lookup", ContentType: "json", Content: `{"name":"lookup"}`},
		{Seq: 4, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "first question"},
		{Seq: 5, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "first answer"},
		{Seq: 6, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemToolResult, Role: "tool", ContentType: "text", ToolCallId: "call-1", Content: "lookup result"},
		{Seq: 7, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "final answer"},
	})
	turn, err = MaterializeAPIRequestLogTurn(db, secondLog, APIRequestLogTurnMeta{
		SessionId: "session-1", TurnId: "turn-1", Protocol: "codex", StartedAt: 100, CompletedAt: 110, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionExact,
		Items: []APIRequestLogTurnItemMeta{
			{Seq: 5, ProviderItemId: "assistant-1", TurnId: "turn-1"},
			{Seq: 6, ProviderItemId: "tool-result-1", TurnId: "turn-1"},
			{Seq: 7, ProviderItemId: "assistant-final-1", TurnId: "turn-1", MessagePhase: "final_answer", ItemStatus: "completed"},
		},
	}, secondItems)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogTurnStatusCompleted, turn.CompletionStatus)
	require.Equal(t, "message.final.completed", turn.CompletionSignal)
	require.Equal(t, int64(110), turn.CompletedAt)
	require.Equal(t, 2, turn.RequestCount)
	require.Equal(t, 7, turn.ItemCount)
	require.Equal(t, 22, turn.PromptTokens)
	require.Equal(t, 5, turn.CompletionTokens)

	turn, err = MaterializeAPIRequestLogTurn(db, secondLog, APIRequestLogTurnMeta{}, nil)
	require.NoError(t, err, "a usage-only retry must inherit the request's existing turn identity")
	require.Equal(t, "turn-1", turn.TurnId)
	require.Equal(t, 2, turn.RequestCount)

	thirdLog, thirdItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 2, TokenName: "prod", ModelName: "gpt-turn", CreatedAt: 120, RequestId: "request-3",
	}, []APIRequestLogItem{
		{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "system", ContentType: "text", Content: "system instructions"},
		{Seq: 2, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "developer", ContentType: "text", Content: "developer instructions"},
		{Seq: 3, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemToolSpec, Name: "lookup", ContentType: "json", Content: `{"name":"lookup"}`},
		{Seq: 4, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "first question"},
		{Seq: 5, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "first answer"},
		{Seq: 6, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemToolResult, Role: "tool", ContentType: "text", ToolCallId: "call-1", Content: "lookup result"},
		{Seq: 7, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "final answer"},
		{Seq: 8, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "second question"},
		{Seq: 9, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "second answer"},
	})
	secondTurn, err := MaterializeAPIRequestLogTurn(db, thirdLog, APIRequestLogTurnMeta{
		SessionId: "session-1", TurnId: "turn-2", Protocol: "codex", StartedAt: 120, CompletedAt: 130, CompletionStatus: APIRequestLogTurnStatusCompleted, CompletionSignal: "provider.stop", Attribution: APIRequestLogTurnAttributionExact,
		Items: []APIRequestLogTurnItemMeta{
			{Seq: 5, ProviderItemId: "assistant-1", TurnId: "turn-1"},
			{Seq: 6, ProviderItemId: "tool-result-1", TurnId: "turn-1"},
			{Seq: 7, ProviderItemId: "assistant-final-1", TurnId: "turn-1"},
			{Seq: 8, ProviderItemId: "user-2", TurnId: "turn-2"},
			{Seq: 9, ProviderItemId: "assistant-2", TurnId: "turn-2", MessagePhase: "final", ItemStatus: "completed"},
		},
	}, thirdItems)
	require.NoError(t, err)
	require.Equal(t, 2, secondTurn.TurnIndex)
	require.Equal(t, 5, secondTurn.ItemCount)
	require.Equal(t, "message.final.completed", secondTurn.CompletionSignal)

	detail, err := GetAPIRequestLogTurnById(db, secondTurn.Id)
	require.NoError(t, err)
	require.Len(t, detail.Items, 5)
	contents := make([]string, 0, len(detail.Items))
	for _, item := range detail.Items {
		contents = append(contents, string(item.Content))
		require.NotEqual(t, "encrypted", item.ContentType)
	}
	joined := strings.Join(contents, "\n")
	require.Contains(t, joined, "system instructions")
	require.Contains(t, joined, "developer instructions")
	require.Contains(t, joined, "second question")
	require.Contains(t, joined, "second answer")
	require.NotContains(t, joined, "first question")
	require.NotContains(t, joined, "final answer")

	turns, total, err := GetAPIRequestLogTurns(db, APIRequestLogTurnQueryParams{StartTimestamp: 110, EndTimestamp: 130, SessionId: "session-1", Num: 20})
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "turn-1", turns[0].TurnId)

	columns, err := db.Migrator().ColumnTypes(&APIRequestLogTurnItem{})
	require.NoError(t, err)
	for _, column := range columns {
		require.NotEqual(t, "content", strings.ToLower(column.Name()))
	}
}

func TestMaterializeAPIRequestLogTurnUnknownAndLateSourceItems(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	log := &APIRequestLog{Username: "bob", ModelName: "gpt-turn", CreatedAt: 200, RequestId: "late-items"}
	require.NoError(t, db.Create(log).Error)
	inMemory := []APIRequestLogItem{{Seq: 1, LogId: log.Id, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "late"}}

	turn, err := MaterializeAPIRequestLogTurn(db, log, APIRequestLogTurnMeta{
		CompletionStatus: APIRequestLogTurnStatusCompleted,
		Attribution:      APIRequestLogTurnAttributionExact,
		Items:            []APIRequestLogTurnItemMeta{{Seq: 1, MessagePhase: "final", ItemStatus: "completed"}},
	}, inMemory)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogTurnStatusUnknown, turn.CompletionStatus)
	require.Equal(t, APIRequestLogTurnAttributionUnknown, turn.Attribution)
	require.Zero(t, turn.ItemCount)

	require.NoError(t, db.Create(&inMemory).Error)
	turn, err = MaterializeAPIRequestLogTurn(db, log, APIRequestLogTurnMeta{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, turn.ItemCount)

	inferredLog, inferredItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{Username: "bob", ModelName: "gpt-turn", CreatedAt: 300}, []APIRequestLogItem{
		{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "inferred"},
	})
	inferred, err := MaterializeAPIRequestLogTurn(db, inferredLog, APIRequestLogTurnMeta{
		SessionId: "synthetic-session", CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionInferred,
	}, inferredItems)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogTurnAttributionInferred, inferred.Attribution)
	require.Equal(t, "inferred-turn:", inferred.TurnId[:len("inferred-turn:")])
}

func TestMaterializeAPIRequestLogTurnReordersOutOfOrderRequestsAndStableIdConflicts(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	earlierLog, earlierItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, TokenId: 2, ModelName: "gpt-turn", CreatedAt: 100, RequestId: "earlier",
	}, []APIRequestLogItem{
		{Seq: 1, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "early regular"},
		{Seq: 2, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "early provider"},
		{Seq: 3, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemToolResult, Role: "tool", ContentType: "text", ToolCallId: "call-shared", Content: "early call"},
	})
	laterLog, laterItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, TokenId: 2, ModelName: "gpt-turn", CreatedAt: 200, RequestId: "later",
	}, []APIRequestLogItem{
		{Seq: 1, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "late provider"},
		{Seq: 2, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemToolResult, Role: "tool", ContentType: "text", ToolCallId: "call-shared", Content: "late call"},
		{Seq: 3, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "late regular"},
	})
	meta := APIRequestLogTurnMeta{
		SessionId: "session-order", TurnId: "turn-order", Protocol: "codex",
		CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionExact,
	}
	laterMeta := meta
	laterMeta.Items = []APIRequestLogTurnItemMeta{{Seq: 1, ProviderItemId: "provider-shared"}}
	_, err := MaterializeAPIRequestLogTurn(db, laterLog, laterMeta, laterItems)
	require.NoError(t, err)
	earlierMeta := meta
	earlierMeta.Items = []APIRequestLogTurnItemMeta{{Seq: 2, ProviderItemId: "provider-shared"}}
	turn, err := MaterializeAPIRequestLogTurn(db, earlierLog, earlierMeta, earlierItems)
	require.NoError(t, err)

	detail, err := GetAPIRequestLogTurnById(db, turn.Id)
	require.NoError(t, err)
	require.Len(t, detail.Requests, 2)
	require.Equal(t, earlierLog.Id, detail.Requests[0].LogId)
	require.Equal(t, 1, detail.Requests[0].Sequence)
	require.Equal(t, laterLog.Id, detail.Requests[1].LogId)
	require.Equal(t, 2, detail.Requests[1].Sequence)

	contents := make([]string, 0, len(detail.Items))
	for index, item := range detail.Items {
		require.Equal(t, index+1, item.Ordinal)
		contents = append(contents, string(item.Content))
	}
	require.Equal(t, []string{"early regular", "early provider", "early call", "late regular"}, contents)
}

func TestMaterializeAPIRequestLogTurnPreservesOrdinaryOccurrencesAndDeduplicatesTurnContext(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	log, items := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, TokenId: 2, ModelName: "gpt-turn", CreatedAt: 300,
	}, []APIRequestLogItem{
		{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "system", ContentType: "text", Content: "system"},
		{Seq: 2, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "system", ContentType: "text", Content: "system"},
		{Seq: 3, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "developer", ContentType: "text", Content: "developer"},
		{Seq: 4, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "developer", ContentType: "text", Content: "developer"},
		{Seq: 5, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemToolSpec, Name: "lookup", ContentType: "json", Content: `{"name":"lookup"}`},
		{Seq: 6, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemToolSpec, Name: "lookup", ContentType: "json", Content: `{"name":"lookup"}`},
		{Seq: 7, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "repeat"},
		{Seq: 8, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "repeat"},
	})
	turn, err := MaterializeAPIRequestLogTurn(db, log, APIRequestLogTurnMeta{
		SessionId: "session-occurrence", TurnId: "turn-occurrence", Protocol: "codex",
		CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionExact,
		Items: []APIRequestLogTurnItemMeta{
			{Seq: 1, ProviderItemId: "system-1"},
			{Seq: 2, ProviderItemId: "system-2"},
			{Seq: 3, ProviderItemId: "developer-1"},
			{Seq: 4, ProviderItemId: "developer-2"},
			{Seq: 5, ProviderItemId: "tool-spec-1"},
			{Seq: 6, ProviderItemId: "tool-spec-2"},
		},
	}, items)
	require.NoError(t, err)
	require.Equal(t, 5, turn.ItemCount)

	detail, err := GetAPIRequestLogTurnById(db, turn.Id)
	require.NoError(t, err)
	counts := map[string]int{}
	keys := map[string]bool{}
	for _, item := range detail.Items {
		counts[string(item.Content)]++
		require.False(t, keys[item.CanonicalKey])
		keys[item.CanonicalKey] = true
	}
	require.Equal(t, 1, counts["system"])
	require.Equal(t, 1, counts["developer"])
	require.Equal(t, 1, counts[`{"name":"lookup"}`])
	require.Equal(t, 2, counts["repeat"])
}

func TestMaterializeAPIRequestLogTurnOwnerFingerprintIsolation(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	meta := APIRequestLogTurnMeta{
		SessionId: "shared-session", TurnId: "shared-turn", Protocol: "codex",
		CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionExact,
	}
	aliceLog, aliceItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 10, TokenName: "alice-token", ModelName: "gpt-turn", CreatedAt: 400,
	}, []APIRequestLogItem{{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "alice"}})
	bobLog, bobItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 2, Username: "bob", TokenId: 20, TokenName: "bob-token", ModelName: "gpt-turn", CreatedAt: 401,
	}, []APIRequestLogItem{{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "bob"}})
	aliceTurn, err := MaterializeAPIRequestLogTurn(db, aliceLog, meta, aliceItems)
	require.NoError(t, err)
	bobTurn, err := MaterializeAPIRequestLogTurn(db, bobLog, meta, bobItems)
	require.NoError(t, err)
	require.NotEqual(t, aliceTurn.Id, bobTurn.Id)
	require.Len(t, aliceTurn.OwnerFingerprint, 64)
	require.NotEqual(t, aliceTurn.OwnerFingerprint, bobTurn.OwnerFingerprint)

	renamedAlice := *aliceLog
	renamedAlice.Id++
	renamedAlice.Username = "alice-renamed"
	renamedAlice.TokenName = "renamed-token"
	require.Equal(t, apiRequestLogOwnerFingerprint(aliceLog), apiRequestLogOwnerFingerprint(&renamedAlice), "stable numeric identities must not depend on display names")

	anonymousOne, anonymousOneItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		ModelName: "gpt-turn", CreatedAt: 402,
	}, []APIRequestLogItem{{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "anonymous one"}})
	anonymousTwo, anonymousTwoItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		ModelName: "gpt-turn", CreatedAt: 403,
	}, []APIRequestLogItem{{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "anonymous two"}})
	anonymousTurnOne, err := MaterializeAPIRequestLogTurn(db, anonymousOne, meta, anonymousOneItems)
	require.NoError(t, err)
	anonymousTurnTwo, err := MaterializeAPIRequestLogTurn(db, anonymousTwo, meta, anonymousTwoItems)
	require.NoError(t, err)
	require.NotEqual(t, anonymousTurnOne.Id, anonymousTurnTwo.Id)
	require.NotEqual(t, anonymousTurnOne.OwnerFingerprint, anonymousTurnTwo.OwnerFingerprint)
	require.Equal(t, APIRequestLogTurnStatusUnknown, anonymousTurnOne.CompletionStatus)
	require.Equal(t, APIRequestLogTurnAttributionUnknown, anonymousTurnOne.Attribution)
}

func TestMaterializeAPIRequestLogTurnFreezesClaimedTurnsAndSessionIndexes(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	log, items := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, TokenId: 2, ModelName: "gpt-turn", CreatedAt: 500, PromptTokens: 10,
	}, []APIRequestLogItem{{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "frozen"}})
	turn, err := MaterializeAPIRequestLogTurn(db, log, APIRequestLogTurnMeta{
		SessionId: "session-frozen", TurnId: "turn-frozen", Protocol: "codex", StartedAt: 500, CompletedAt: 500,
		CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionExact,
	}, items)
	require.NoError(t, err)
	batch := APIRequestLogExportBatch{Tag: "turn-export-test-frozen", Status: APIRequestLogExportBatchStatusCompleted, SchemaVersion: APIRequestLogExportSchemaVersion}
	require.NoError(t, db.Create(&batch).Error)
	require.NoError(t, db.Create(&APIRequestLogExportMember{BatchId: batch.Id, TurnRecordId: turn.Id, Sequence: 1}).Error)

	lateLog, lateItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, TokenId: 2, ModelName: "gpt-turn", CreatedAt: 600, PromptTokens: 99,
	}, []APIRequestLogItem{{Seq: 1, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "must not be added"}})
	frozen, err := MaterializeAPIRequestLogTurn(db, lateLog, APIRequestLogTurnMeta{
		SessionId: "session-frozen", TurnId: "turn-frozen", Protocol: "codex", StartedAt: 500, CompletedAt: 600,
		CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionExact,
	}, lateItems)
	require.NoError(t, err)
	require.Equal(t, turn.Id, frozen.Id)
	require.Equal(t, 1, frozen.RequestCount)
	require.Equal(t, 1, frozen.ItemCount)
	require.Equal(t, 10, frozen.PromptTokens)

	earlierLog, earlierItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, TokenId: 2, ModelName: "gpt-turn", CreatedAt: 400,
	}, []APIRequestLogItem{{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "chronologically earlier"}})
	earlierTurn, err := MaterializeAPIRequestLogTurn(db, earlierLog, APIRequestLogTurnMeta{
		SessionId: "session-frozen", TurnId: "turn-created-later", Protocol: "codex", StartedAt: 400, CompletedAt: 400,
		CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionExact,
	}, earlierItems)
	require.NoError(t, err)
	require.Equal(t, 2, earlierTurn.TurnIndex, "an exported session must retain its existing turn indexes")
	var persistedFrozen APIRequestLogTurn
	require.NoError(t, db.First(&persistedFrozen, turn.Id).Error)
	require.Equal(t, 1, persistedFrozen.TurnIndex)
}
