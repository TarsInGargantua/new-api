package model

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOrganizerProviderTurnID(t *testing.T) {
	items := []APIRequestLogItem{
		{Phase: APIRequestLogPhaseOutput, ContentType: "json", Content: `{"type":"response.function_call_arguments.delta","delta":"x","metadata":{"turn_id":"ignored"}}`, Source: "sse.response.function_call_arguments.delta"},
		{Phase: APIRequestLogPhaseOutput, ContentType: "json", Content: `{"type":"function_call","status":"completed","metadata":{"turn_id":"turn-1"}}`, Source: "sse.item"},
	}
	turnID, ambiguous := organizerProviderTurnID(items)
	if ambiguous || turnID != "turn-1" {
		t.Fatalf("organizerProviderTurnID() = %q, %v", turnID, ambiguous)
	}

	items = append(items, APIRequestLogItem{Phase: APIRequestLogPhaseOutput, ContentType: "json", Content: `{"metadata":{"turn_id":"turn-2"}}`})
	if _, ambiguous := organizerProviderTurnID(items); !ambiguous {
		t.Fatal("multiple provider turn ids must be ambiguous")
	}
}

func TestOrganizerCompareUserSequences(t *testing.T) {
	tests := []struct {
		name     string
		previous []string
		current  []string
		want     apiRequestLogUserSequenceRelation
	}{
		{name: "same", previous: []string{"a"}, current: []string{"a"}, want: apiRequestLogUserSequenceSame},
		{name: "appended", previous: []string{"a"}, current: []string{"a", "b"}, want: apiRequestLogUserSequenceAppended},
		{name: "stale", previous: []string{"a", "b"}, current: []string{"a"}, want: apiRequestLogUserSequenceStale},
		{name: "diverged", previous: []string{"a"}, current: []string{"b"}, want: apiRequestLogUserSequenceDiverged},
		{name: "missing", previous: nil, current: []string{"b"}, want: apiRequestLogUserSequenceUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := organizerCompareUserSequences(test.previous, test.current); got != test.want {
				t.Fatalf("organizerCompareUserSequences() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestOrganizerCanonicalItemsDropsDeltasAndEncrypted(t *testing.T) {
	items := []APIRequestLogItem{
		{Id: 1, Seq: 1, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemToolCall, ContentType: "json", Source: "sse.item", Content: `{"id":"item-1","status":"in_progress"}`},
		{Id: 2, Seq: 2, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemToolCall, ContentType: "json", Source: "sse.response.function_call_arguments.delta", Content: `{"item_id":"item-1","delta":"x"}`},
		{Id: 3, Seq: 3, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemToolCall, ContentType: "json", Source: "sse.item", Content: `{"id":"item-1","status":"completed","phase":"commentary","metadata":{"turn_id":"turn-1"}}`},
		{Id: 4, Seq: 4, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemReasoning, ContentType: "encrypted", Content: "ciphertext"},
		{Id: 5, Seq: 5, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, ContentType: "text", Content: "done"},
	}
	got := organizerCanonicalItems(items)
	if len(got) != 2 {
		t.Fatalf("organizerCanonicalItems() returned %d items, want 2", len(got))
	}
	if got[0].Source.Id != 3 || got[0].MessagePhase != "commentary" || got[0].ItemStatus != "completed" || got[0].TurnID != "turn-1" {
		t.Fatalf("unexpected completed tool item: %+v", got[0])
	}
	meta := organizerTurnItemMeta(got)
	if len(meta) != 2 || meta[0].TurnId != "turn-1" {
		t.Fatalf("unexpected turn item metadata: %+v", meta)
	}
	if got[1].Source.Id != 5 {
		t.Fatalf("unexpected message item: %+v", got[1])
	}
}

func TestOrganizerSyntheticIDsAreStable(t *testing.T) {
	identity := apiRequestLogOrganizerIdentity{UserKey: "id:1", TokenKey: "id:2", Model: "gpt-test"}
	first := organizerSyntheticSessionID(identity, 10)
	if first != organizerSyntheticSessionID(identity, 10) {
		t.Fatal("synthetic session id is not stable")
	}
	if first == organizerSyntheticSessionID(identity, 11) {
		t.Fatal("different session starts must not share an id")
	}
	if organizerSyntheticTurnID(first, 10) == organizerSyntheticTurnID(first, 11) {
		t.Fatal("different turn starts must not share an id")
	}
}

func TestAPIRequestLogOrganizerTrackerScopesTurnsToOwner(t *testing.T) {
	tracker := apiRequestLogOrganizerTracker{turns: make(map[string]apiRequestLogOrganizerTurnObservation)}
	meta := APIRequestLogTurnMeta{
		SessionId: "shared-session", TurnId: "shared-turn",
		Attribution: APIRequestLogTurnAttributionExact, CompletionStatus: APIRequestLogTurnStatusCompleted,
	}
	tracker.observe("owner-a", meta)
	tracker.observe("owner-b", meta)

	var stats APIRequestLogOrganizerStats
	tracker.writeStats(&stats)
	require.Equal(t, int64(2), stats.Exact)
	require.Equal(t, int64(2), stats.Completed)
}

func TestOrganizeAPIRequestLogTurnsDryRunCountsFinalCompletedItem(t *testing.T) {
	db := setupAPIRequestLogOrganizerTestDB(t)
	base := int64(1_779_900_000)
	createAPIRequestLogOrganizerFixture(t, db, base, []APIRequestLogItem{
		{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "hello"},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "json", Source: "sse.item", Content: `{"id":"message-1","status":"completed","phase":"final","metadata":{"turn_id":"turn-1"}}`},
	})

	stats, err := OrganizeAPIRequestLogTurns(t.Context(), db, APIRequestLogOrganizerOptions{
		BatchSize: 10, LagSeconds: 0, DryRun: true,
		now: func() time.Time { return time.Unix(base+100, 0) },
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.Inferred)
	require.Equal(t, int64(1), stats.Completed)
	require.Zero(t, stats.Open)
}

func TestOrganizeAPIRequestLogTurnsRepairsExistingFinalAnswerCompletion(t *testing.T) {
	db := setupAPIRequestLogOrganizerTestDB(t)
	base := int64(1_779_950_000)
	log := createAPIRequestLogOrganizerFixture(t, db, base, []APIRequestLogItem{
		{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "hello"},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "done"},
	})
	var items []APIRequestLogItem
	require.NoError(t, db.Where("log_id = ?", log.Id).Order("seq ASC").Find(&items).Error)
	turn, err := MaterializeAPIRequestLogTurn(db, &log, APIRequestLogTurnMeta{
		SessionId: "session-live", TurnId: "turn-live", Protocol: "openai_responses",
		CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionExact,
		Items: []APIRequestLogTurnItemMeta{{Seq: 2, ProviderItemId: "message-live", TurnId: "turn-live", MessagePhase: "commentary", ItemStatus: "completed"}},
	}, items)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogTurnStatusOpen, turn.CompletionStatus)
	require.NoError(t, db.Model(&APIRequestLogTurnItem{}).
		Where("turn_record_id = ? AND provider_item_id = ?", turn.Id, "message-live").
		Updates(map[string]interface{}{"message_phase": "final_answer", "item_status": "completed"}).Error)

	stats, err := OrganizeAPIRequestLogTurns(t.Context(), db, APIRequestLogOrganizerOptions{
		BatchSize: 10, LagSeconds: 0, IgnoreProgress: true,
		now: func() time.Time { return time.Unix(base+100, 0) },
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.Completed)
	require.NoError(t, db.First(turn, turn.Id).Error)
	require.Equal(t, APIRequestLogTurnStatusCompleted, turn.CompletionStatus)
	require.Equal(t, log.CreatedAt, turn.CompletedAt)
	require.Equal(t, "message.final.completed", turn.CompletionSignal)

	var mapping APIRequestLogTurnItem
	require.NoError(t, db.Where("turn_record_id = ? AND provider_item_id = ?", turn.Id, "message-live").First(&mapping).Error)
	require.Equal(t, "final_answer", mapping.MessagePhase)
	require.Equal(t, "completed", mapping.ItemStatus)
}

func TestOrganizeAPIRequestLogTurnsAcrossBatchesAndIdempotently(t *testing.T) {
	db := setupAPIRequestLogOrganizerTestDB(t)
	base := int64(1_780_000_000)
	first := createAPIRequestLogOrganizerFixture(t, db, base, []APIRequestLogItem{
		{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "hello"},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemToolCall, Role: "assistant", ContentType: "json", Source: "sse.item", Content: `{"id":"provider-item-1","status":"in_progress","metadata":{"turn_id":"turn-1"}}`},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemToolCall, Role: "assistant", ContentType: "json", Source: "sse.response.function_call_arguments.delta", Content: `{"item_id":"provider-item-1","delta":"x"}`},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemToolCall, Role: "assistant", ContentType: "json", Source: "sse.item", Content: `{"id":"provider-item-1","status":"completed","metadata":{"turn_id":"turn-1"}}`},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemReasoning, Role: "assistant", ContentType: "encrypted", Content: "ciphertext"},
	})
	second := createAPIRequestLogOrganizerFixture(t, db, base+10, []APIRequestLogItem{
		{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "hello"},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "working"},
	})
	createAPIRequestLogOrganizerFixture(t, db, base+20, []APIRequestLogItem{
		{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "hello"},
		{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "next"},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemToolCall, Role: "assistant", ContentType: "json", Source: "sse.item", Content: `{"id":"provider-item-2","status":"completed","metadata":{"turn_id":"turn-2"}}`},
	})
	createAPIRequestLogOrganizerFixture(t, db, base+30, []APIRequestLogItem{
		{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "unrelated"},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "answer"},
	})
	createAPIRequestLogOrganizerFixture(t, db, base+1900, []APIRequestLogItem{
		{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "fresh window"},
		{Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "answer"},
	})

	options := APIRequestLogOrganizerOptions{
		BatchSize:  2,
		LagSeconds: 0,
		now: func() time.Time {
			return time.Unix(base+10_000, 0)
		},
	}
	stats, err := OrganizeAPIRequestLogTurns(t.Context(), db, options)
	require.NoError(t, err)
	require.Equal(t, int64(5), stats.Requests)
	require.Equal(t, int64(3), stats.Inferred)
	require.Equal(t, int64(1), stats.Unknown)
	require.Equal(t, int64(2), stats.Open)
	require.Equal(t, int64(1), stats.Completed)

	var turns []APIRequestLogTurn
	require.NoError(t, db.Order("started_at ASC").Find(&turns).Error)
	require.Len(t, turns, 4)
	turnByID := make(map[string]APIRequestLogTurn)
	for _, turn := range turns {
		turnByID[turn.TurnId] = turn
	}
	require.Equal(t, APIRequestLogTurnStatusCompleted, turnByID["turn-1"].CompletionStatus)
	require.Equal(t, second.CreatedAt, turnByID["turn-1"].CompletedAt)
	require.Equal(t, 2, turnByID["turn-1"].RequestCount)
	require.Equal(t, APIRequestLogTurnStatusOpen, turnByID["turn-2"].CompletionStatus)
	require.Equal(t, APIRequestLogTurnAttributionUnknown, turns[2].Attribution)
	require.NotEqual(t, turnByID["turn-2"].SessionId, turns[3].SessionId)

	var mappedForbidden int64
	require.NoError(t, db.Table(apiRequestLogTurnItemsTable+" mapped").
		Joins("JOIN api_request_log_items source_item ON source_item.id = mapped.source_item_id").
		Where("source_item.content_type = ? OR source_item.source LIKE ?", "encrypted", "%.delta%").
		Count(&mappedForbidden).Error)
	require.Zero(t, mappedForbidden)

	var requestCountBefore, itemCountBefore int64
	require.NoError(t, db.Model(&APIRequestLogTurnRequest{}).Count(&requestCountBefore).Error)
	require.NoError(t, db.Model(&APIRequestLogTurnItem{}).Count(&itemCountBefore).Error)
	_, err = OrganizeAPIRequestLogTurns(t.Context(), db, options)
	require.NoError(t, err)
	var requestCountAfter, itemCountAfter int64
	require.NoError(t, db.Model(&APIRequestLogTurnRequest{}).Count(&requestCountAfter).Error)
	require.NoError(t, db.Model(&APIRequestLogTurnItem{}).Count(&itemCountAfter).Error)
	require.Equal(t, requestCountBefore, requestCountAfter)
	require.Equal(t, itemCountBefore, itemCountAfter)
	require.Positive(t, first.Id)
}

func TestOrganizeAPIRequestLogTurnsDryRunHonorsAfterIDAndMaxRows(t *testing.T) {
	db := setupAPIRequestLogOrganizerTestDB(t)
	base := int64(1_780_100_000)
	first := createAPIRequestLogOrganizerFixture(t, db, base, []APIRequestLogItem{{
		Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "one",
	}})
	createAPIRequestLogOrganizerFixture(t, db, base+1, []APIRequestLogItem{{
		Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "one",
	}})
	last := createAPIRequestLogOrganizerFixture(t, db, base+2, []APIRequestLogItem{{
		Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "one",
	}})
	require.NoError(t, db.Create(&APIRequestLogOrganizerState{
		Name: apiRequestLogOrganizerStateKey, LastLogId: int64(last.Id),
	}).Error)
	require.NoError(t, db.Migrator().DropTable(&APIRequestLogTurnItem{}, &APIRequestLogTurnRequest{}, &APIRequestLogTurn{}))

	stats, err := OrganizeAPIRequestLogTurns(t.Context(), db, APIRequestLogOrganizerOptions{
		BatchSize: 1, AfterID: int64(first.Id), MaxRows: 1, LagSeconds: 0, DryRun: true,
		now: func() time.Time { return time.Unix(base+100, 0) },
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.Requests)
	require.Equal(t, int64(first.Id+1), stats.LastID)
	require.False(t, db.Migrator().HasTable(&APIRequestLogTurn{}))

	fromStart, err := OrganizeAPIRequestLogTurns(t.Context(), db, APIRequestLogOrganizerOptions{
		BatchSize: 1, MaxRows: 1, LagSeconds: 0, DryRun: true,
		now: func() time.Time { return time.Unix(base+100, 0) },
	})
	require.NoError(t, err)
	require.Equal(t, int64(first.Id), fromStart.LastID, "dry-run must ignore persisted progress")
	progress, err := loadAPIRequestLogOrganizerProgress(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(last.Id), progress, "dry-run must not update persisted progress")
}

func TestOrganizeAPIRequestLogTurnsResumesFromPersistedProgress(t *testing.T) {
	db := setupAPIRequestLogOrganizerTestDB(t)
	base := int64(1_780_200_000)
	logs := make([]APIRequestLog, 0, 3)
	for idx := 0; idx < 3; idx++ {
		logs = append(logs, createAPIRequestLogOrganizerFixture(t, db, base+int64(idx), []APIRequestLogItem{{
			Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "one",
		}}))
	}
	now := func() time.Time { return time.Unix(base+100, 0) }

	first, err := OrganizeAPIRequestLogTurns(t.Context(), db, APIRequestLogOrganizerOptions{
		BatchSize: 1, MaxRows: 1, LagSeconds: 0, now: now,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Requests)
	require.Equal(t, int64(logs[0].Id), first.LastID)
	progress, err := loadAPIRequestLogOrganizerProgress(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(logs[0].Id), progress)

	second, err := OrganizeAPIRequestLogTurns(t.Context(), db, APIRequestLogOrganizerOptions{
		BatchSize: 1, LagSeconds: 0, now: now,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Requests)
	require.Equal(t, int64(logs[2].Id), second.LastID)
	progress, err = loadAPIRequestLogOrganizerProgress(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(logs[2].Id), progress)

	overridden, err := OrganizeAPIRequestLogTurns(t.Context(), db, APIRequestLogOrganizerOptions{
		BatchSize: 1, AfterID: int64(logs[0].Id), MaxRows: 1, LagSeconds: 0, now: now,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), overridden.Requests)
	require.Equal(t, int64(logs[1].Id), overridden.LastID)

	rescanned, err := OrganizeAPIRequestLogTurns(t.Context(), db, APIRequestLogOrganizerOptions{
		BatchSize: 1, MaxRows: 1, LagSeconds: 0, IgnoreProgress: true, now: now,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), rescanned.Requests)
	require.Equal(t, int64(logs[0].Id), rescanned.LastID)
	progress, err = loadAPIRequestLogOrganizerProgress(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(logs[2].Id), progress, "manual rescans must not move persisted progress backwards")

	var requestCount int64
	require.NoError(t, db.Model(&APIRequestLogTurnRequest{}).Count(&requestCount).Error)
	require.Equal(t, int64(3), requestCount)
}

func TestOrganizeAPIRequestLogTurnsRollsBackBeforeAdvancingProgress(t *testing.T) {
	db := setupAPIRequestLogOrganizerTestDB(t)
	base := int64(1_780_300_000)
	createAPIRequestLogOrganizerFixture(t, db, base, []APIRequestLogItem{{
		Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "one",
	}})
	require.NoError(t, db.Create(&APIRequestLogOrganizerState{Name: apiRequestLogOrganizerStateKey}).Error)
	table := APIRequestLogOrganizerState{}.TableName()
	require.NoError(t, db.Exec(fmt.Sprintf(
		"CREATE TRIGGER fail_organizer_progress BEFORE UPDATE OF last_log_id ON %s BEGIN SELECT RAISE(ABORT, 'forced progress failure'); END",
		table,
	)).Error)

	_, err := OrganizeAPIRequestLogTurns(t.Context(), db, APIRequestLogOrganizerOptions{
		BatchSize: 1, LagSeconds: 0, now: func() time.Time { return time.Unix(base+100, 0) },
	})
	require.ErrorContains(t, err, "forced progress failure")

	progress, progressErr := loadAPIRequestLogOrganizerProgress(t.Context(), db)
	require.NoError(t, progressErr)
	require.Zero(t, progress)
	var requestCount int64
	require.NoError(t, db.Model(&APIRequestLogTurnRequest{}).Count(&requestCount).Error)
	require.Zero(t, requestCount, "turn mappings must roll back with the failed progress update")
}

func TestApplyAPIRequestLogOrganizerDecisionScopesActionsToOwner(t *testing.T) {
	db := setupAPIRequestLogOrganizerTestDB(t)
	firstLog, _ := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 11, TokenName: "first", ModelName: "gpt-test", CreatedAt: 100,
	}, nil)
	renameLog, _ := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 11, TokenName: "first", ModelName: "gpt-test", CreatedAt: 101,
	}, nil)
	secondLog, _ := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 2, Username: "bob", TokenId: 22, TokenName: "second", ModelName: "gpt-test", CreatedAt: 100,
	}, nil)
	firstOwner := apiRequestLogOwnerFingerprint(firstLog)
	secondOwner := apiRequestLogOwnerFingerprint(secondLog)
	require.NotEqual(t, firstOwner, secondOwner)

	turns := []APIRequestLogTurn{
		{OwnerFingerprint: firstOwner, SessionId: "shared-session", TurnId: "close-turn", Protocol: "responses", TurnIndex: 1, StartedAt: 90, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred},
		{OwnerFingerprint: secondOwner, SessionId: "shared-session", TurnId: "close-turn", Protocol: "responses", TurnIndex: 1, StartedAt: 90, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred},
		{OwnerFingerprint: firstOwner, SessionId: "shared-session", TurnId: "rename-turn", Protocol: "responses", TurnIndex: 2, StartedAt: 100, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred},
		{OwnerFingerprint: secondOwner, SessionId: "shared-session", TurnId: "rename-turn", Protocol: "responses", TurnIndex: 2, StartedAt: 100, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred},
	}
	require.NoError(t, db.Create(&turns).Error)

	closeDecision := apiRequestLogOrganizerDecision{
		Log: *firstLog,
		Meta: APIRequestLogTurnMeta{
			SessionId: "shared-session", TurnId: "close-turn", Protocol: "responses", StartedAt: 90,
			CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred,
		},
		Close: &apiRequestLogOrganizerCloseAction{
			OwnerFingerprint: firstOwner, SessionID: "shared-session", TurnID: "close-turn", CompletedAt: 110,
		},
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return applyAPIRequestLogOrganizerDecision(tx, &closeDecision)
	}))

	var firstClosed, secondOpen APIRequestLogTurn
	require.NoError(t, db.Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", firstOwner, "shared-session", "close-turn").First(&firstClosed).Error)
	require.NoError(t, db.Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", secondOwner, "shared-session", "close-turn").First(&secondOpen).Error)
	require.Equal(t, APIRequestLogTurnStatusCompleted, firstClosed.CompletionStatus)
	require.Equal(t, int64(110), firstClosed.CompletedAt)
	require.Equal(t, APIRequestLogTurnStatusOpen, secondOpen.CompletionStatus)
	require.Zero(t, secondOpen.CompletedAt)

	renameDecision := apiRequestLogOrganizerDecision{
		Log: *renameLog,
		Meta: APIRequestLogTurnMeta{
			SessionId: "shared-session", TurnId: "renamed-turn", Protocol: "responses", StartedAt: 100,
			CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred,
		},
		Rename: &apiRequestLogOrganizerRenameAction{
			OwnerFingerprint: firstOwner, SessionID: "shared-session", FromTurnID: "rename-turn", ToTurnID: "renamed-turn",
		},
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return applyAPIRequestLogOrganizerDecision(tx, &renameDecision)
	}))

	var firstRenamed, secondUnchanged APIRequestLogTurn
	require.NoError(t, db.Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", firstOwner, "shared-session", "renamed-turn").First(&firstRenamed).Error)
	require.NoError(t, db.Where("owner_fingerprint = ? AND session_id = ? AND turn_id = ?", secondOwner, "shared-session", "rename-turn").First(&secondUnchanged).Error)
	require.Equal(t, firstOwner, firstRenamed.OwnerFingerprint)
	require.Equal(t, "rename-turn", secondUnchanged.TurnId)
}

func TestApplyAPIRequestLogOrganizerDecisionDoesNotMutateExportedTurns(t *testing.T) {
	db := setupAPIRequestLogOrganizerTestDB(t)
	closeLog, _ := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 11, TokenName: "prod", ModelName: "gpt-test", CreatedAt: 100,
	}, nil)
	renameLog, _ := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 11, TokenName: "prod", ModelName: "gpt-test", CreatedAt: 101,
	}, nil)
	ownerFingerprint := apiRequestLogOwnerFingerprint(closeLog)
	turns := []APIRequestLogTurn{
		{OwnerFingerprint: ownerFingerprint, SessionId: "session", TurnId: "close-turn", Protocol: "responses", TurnIndex: 1, StartedAt: 90, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred},
		{OwnerFingerprint: ownerFingerprint, SessionId: "session", TurnId: "rename-turn", Protocol: "responses", TurnIndex: 2, StartedAt: 95, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred},
	}
	require.NoError(t, db.Create(&turns).Error)
	batch := APIRequestLogExportBatch{Tag: "turn-export-test", Status: APIRequestLogExportBatchStatusCompleted}
	require.NoError(t, db.Create(&batch).Error)
	require.NoError(t, db.Create(&[]APIRequestLogExportMember{
		{BatchId: batch.Id, TurnRecordId: turns[0].Id, Sequence: 1},
		{BatchId: batch.Id, TurnRecordId: turns[1].Id, Sequence: 2},
	}).Error)

	closeDecision := apiRequestLogOrganizerDecision{
		Log: *closeLog,
		Meta: APIRequestLogTurnMeta{
			SessionId: "session", TurnId: "close-turn", Protocol: "responses", StartedAt: 90,
			CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred,
		},
		Close: &apiRequestLogOrganizerCloseAction{
			OwnerFingerprint: ownerFingerprint, SessionID: "session", TurnID: "close-turn", CompletedAt: 110,
		},
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return applyAPIRequestLogOrganizerDecision(tx, &closeDecision)
	}))

	renameDecision := apiRequestLogOrganizerDecision{
		Log: *renameLog,
		Meta: APIRequestLogTurnMeta{
			SessionId: "session", TurnId: "renamed-turn", Protocol: "responses", StartedAt: 95,
			CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred,
		},
		Rename: &apiRequestLogOrganizerRenameAction{
			OwnerFingerprint: ownerFingerprint, SessionID: "session", FromTurnID: "rename-turn", ToTurnID: "renamed-turn",
		},
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return applyAPIRequestLogOrganizerDecision(tx, &renameDecision)
	}))

	var closed, renamed APIRequestLogTurn
	require.NoError(t, db.First(&closed, turns[0].Id).Error)
	require.NoError(t, db.First(&renamed, turns[1].Id).Error)
	require.Equal(t, APIRequestLogTurnStatusOpen, closed.CompletionStatus)
	require.Zero(t, closed.CompletedAt)
	require.Equal(t, "rename-turn", renamed.TurnId)

	var memberCount int64
	require.NoError(t, db.Model(&APIRequestLogExportMember{}).Where("turn_record_id IN ?", []int64{turns[0].Id, turns[1].Id}).Count(&memberCount).Error)
	require.Equal(t, int64(2), memberCount)
}

func TestRestoreAPIRequestLogOrganizerSessionScopesKnownTurnsToOwner(t *testing.T) {
	db := setupAPIRequestLogOrganizerTestDB(t)
	firstLog, _ := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 11, TokenName: "first", ModelName: "gpt-test", CreatedAt: 100,
	}, []APIRequestLogItem{{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "hello"}})
	secondLog, _ := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 2, Username: "bob", TokenId: 22, TokenName: "second", ModelName: "gpt-test", CreatedAt: 101,
	}, []APIRequestLogItem{{Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "other"}})
	currentLog, _ := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, Username: "alice", TokenId: 11, TokenName: "first", ModelName: "gpt-test", CreatedAt: 102,
	}, nil)
	firstOwner := apiRequestLogOwnerFingerprint(firstLog)
	secondOwner := apiRequestLogOwnerFingerprint(secondLog)
	turns := []APIRequestLogTurn{
		{OwnerFingerprint: firstOwner, SessionId: "inferred-session-shared", TurnId: "first-turn", Protocol: "responses", TurnIndex: 1, StartedAt: 100, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred, UserId: 1, Username: "alice", TokenId: 11, TokenName: "first", ModelName: "gpt-test"},
		{OwnerFingerprint: secondOwner, SessionId: "inferred-session-shared", TurnId: "second-turn", Protocol: "responses", TurnIndex: 1, StartedAt: 101, CompletionStatus: APIRequestLogTurnStatusOpen, Attribution: APIRequestLogTurnAttributionInferred, UserId: 2, Username: "bob", TokenId: 22, TokenName: "second", ModelName: "gpt-test"},
	}
	require.NoError(t, db.Create(&turns).Error)
	require.NoError(t, db.Create(&[]APIRequestLogTurnRequest{
		{TurnRecordId: turns[0].Id, LogId: firstLog.Id, Sequence: 1, CreatedAt: firstLog.CreatedAt},
		{TurnRecordId: turns[1].Id, LogId: secondLog.Id, Sequence: 1, CreatedAt: secondLog.CreatedAt},
	}).Error)

	identity, ok := organizerIdentityForLog(*currentLog)
	require.True(t, ok)
	state, err := restoreAPIRequestLogOrganizerSession(t.Context(), db, identity, *currentLog)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, firstOwner, apiRequestLogOwnerFingerprint(currentLog))
	require.Equal(t, "first-turn", state.CurrentTurnID)
	require.Equal(t, map[string]string{"first-turn": APIRequestLogTurnStatusOpen}, state.KnownTurns)
}

func setupAPIRequestLogOrganizerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "organizer.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&APIRequestLog{},
		&APIRequestLogItem{},
		&APIRequestLogOrganizerState{},
	))
	require.NoError(t, EnsureAPIRequestLogMaterializedTables(db))
	return db
}

func createAPIRequestLogOrganizerFixture(t *testing.T, db *gorm.DB, createdAt int64, items []APIRequestLogItem) APIRequestLog {
	t.Helper()
	log := APIRequestLog{
		Source: APIRequestLogSourceLive, UserId: 7, Username: "alice", TokenId: 9, TokenName: "prod",
		ModelName: "gpt-test", CreatedAt: createdAt, APIFormat: "openai_responses", StatusCode: 200,
	}
	require.NoError(t, db.Create(&log).Error)
	for idx := range items {
		items[idx].LogId = log.Id
		items[idx].Seq = idx + 1
	}
	if len(items) > 0 {
		require.NoError(t, db.Create(&items).Error)
	}
	return log
}
