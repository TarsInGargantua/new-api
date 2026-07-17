package model

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func createAPIRequestLogExportTestTurn(t *testing.T, db *gorm.DB, sessionId, turnId, status, attribution string, completedAt int64) APIRequestLogTurn {
	t.Helper()
	turn := APIRequestLogTurn{
		SessionId: sessionId, TurnId: turnId, Protocol: "codex", TurnIndex: 1,
		StartedAt: completedAt - 10, CompletedAt: completedAt, CompletionStatus: status, Attribution: attribution,
		Username: "alice", ModelName: "gpt-export", ItemCount: 1,
	}
	require.NoError(t, db.Create(&turn).Error)
	return turn
}

func TestAPIRequestLogExportBatchClaimsFullFilterWithoutDuplicates(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	first := createAPIRequestLogExportTestTurn(t, db, "session-1", "turn-1", APIRequestLogTurnStatusCompleted, APIRequestLogTurnAttributionExact, 100)
	second := createAPIRequestLogExportTestTurn(t, db, "session-2", "turn-2", APIRequestLogTurnStatusCompleted, APIRequestLogTurnAttributionExact, 200)
	inferred := createAPIRequestLogExportTestTurn(t, db, "session-3", "turn-3", APIRequestLogTurnStatusCompleted, APIRequestLogTurnAttributionInferred, 150)
	createAPIRequestLogExportTestTurn(t, db, "session-4", "turn-4", APIRequestLogTurnStatusCompleted, APIRequestLogTurnAttributionUnknown, 175)
	createAPIRequestLogExportTestTurn(t, db, "session-5", "turn-5", APIRequestLogTurnStatusOpen, APIRequestLogTurnAttributionExact, 180)

	filter := APIRequestLogTurnQueryParams{StartTimestamp: 100, EndTimestamp: 201, ModelName: "gpt-export", StartIdx: 1, Num: 1}
	preview, err := PreviewAPIRequestLogExport(db, filter, false)
	require.NoError(t, err)
	require.Equal(t, int64(2), preview.MatchedCount)
	require.Equal(t, int64(2), preview.AvailableCount)
	require.Equal(t, int64(2), preview.ExactCount)
	require.Zero(t, preview.InferredCount)

	batch, err := CreateAPIRequestLogExportBatch(db, filter, false)
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^turn-export-\d{8}T\d{6}\.\d{3}Z-[0-9a-f]{6}$`), batch.Tag)
	require.Equal(t, APIRequestLogExportBatchStatusPending, batch.Status)
	require.Equal(t, int64(2), batch.RowCount, "list pagination must not limit a batch")
	require.Equal(t, second.Id, batch.CutoffTurnId)
	require.Equal(t, int64(100), batch.Filter.StartTimestamp)
	require.False(t, batch.IncludeInferred)

	var members []APIRequestLogExportMember
	require.NoError(t, db.Where("batch_id = ?", batch.Id).Order("sequence ASC").Find(&members).Error)
	require.Len(t, members, 2)
	claimed := []int64{members[0].TurnRecordId, members[1].TurnRecordId}
	sort.Slice(claimed, func(i, j int) bool { return claimed[i] < claimed[j] })
	expected := []int64{first.Id, second.Id}
	sort.Slice(expected, func(i, j int) bool { return expected[i] < expected[j] })
	require.Equal(t, expected, claimed)

	secondBatch, err := CreateAPIRequestLogExportBatch(db, filter, false)
	require.NoError(t, err)
	require.Zero(t, secondBatch.RowCount)
	preview, err = PreviewAPIRequestLogExport(db, filter, false)
	require.NoError(t, err)
	require.Zero(t, preview.AvailableCount)
	require.Equal(t, int64(2), preview.AlreadyExportedCount)

	inferredBatch, err := CreateAPIRequestLogExportBatch(db, filter, true)
	require.NoError(t, err)
	require.Equal(t, int64(1), inferredBatch.RowCount)
	var inferredMember APIRequestLogExportMember
	require.NoError(t, db.Where("batch_id = ?", inferredBatch.Id).First(&inferredMember).Error)
	require.Equal(t, inferred.Id, inferredMember.TurnRecordId)

	page, err := GetAPIRequestLogExportBatchTurnPage(db, batch.Id, 0, 1)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.True(t, page.HasMore)
	require.Positive(t, page.NextSequence)
	nextPage, err := GetAPIRequestLogExportBatchTurnPage(db, batch.Id, page.NextSequence, 10)
	require.NoError(t, err)
	require.Len(t, nextPage.Items, 1)
	require.False(t, nextPage.HasMore)

	building, err := ClaimAPIRequestLogExportBatch(db, batch.Tag, "worker-a", time.Minute)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogExportBatchStatusBuilding, building.Status)
	completed, err := MarkAPIRequestLogExportBatchCompleted(db, batch.Tag, "worker-a", "/tmp/export.jsonl", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", batch.RowCount)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogExportBatchStatusCompleted, completed.Status)
	require.Equal(t, "/tmp/export.jsonl", completed.ArtifactPath)
	require.NotZero(t, completed.CompletedAt)
	_, err = RetryAPIRequestLogExportBatchPending(db, batch.Tag)
	require.Error(t, err)

	secondBuilding, err := ClaimAPIRequestLogExportBatch(db, secondBatch.Tag, "worker-a", time.Minute)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogExportBatchStatusBuilding, secondBuilding.Status)
	_, err = RetryAPIRequestLogExportBatchPending(db, secondBatch.Tag)
	require.ErrorIs(t, err, ErrAPIRequestLogExportBatchNotClaimable)
	failed, err := MarkAPIRequestLogExportBatchFailed(db, secondBatch.Tag, "worker-a", errors.New("disk full"))
	require.NoError(t, err)
	require.Equal(t, APIRequestLogExportBatchStatusFailed, failed.Status)
	require.Equal(t, "disk full", failed.Error)
	recovery, err := GetAPIRequestLogExportBatchesForRecovery(db, 20)
	require.NoError(t, err)
	require.NotEmpty(t, recovery)
	retried, err := RetryAPIRequestLogExportBatchPending(db, secondBatch.Tag)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogExportBatchStatusPending, retried.Status)
	require.Zero(t, retried.RowCount)

	allBatches, total, err := GetAPIRequestLogExportBatches(db, APIRequestLogExportBatchQueryParams{Num: 20})
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.Len(t, allBatches, 3)
}

func TestAPIRequestLogExportConcurrentBatchesGloballyClaimTurnOnce(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	for index := 0; index < 8; index++ {
		createAPIRequestLogExportTestTurn(t, db, "concurrent-session", "turn-"+time.Unix(int64(index), 0).UTC().Format("150405"), APIRequestLogTurnStatusCompleted, APIRequestLogTurnAttributionExact, int64(100+index))
	}

	start := make(chan struct{})
	results := make(chan *APIRequestLogExportBatch, 2)
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			batch, err := CreateAPIRequestLogExportBatch(db, APIRequestLogTurnQueryParams{ModelName: "gpt-export"}, false)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- batch
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	var rowCounts []int64
	for batch := range results {
		rowCounts = append(rowCounts, batch.RowCount)
	}
	require.Len(t, rowCounts, 2)
	sort.Slice(rowCounts, func(i, j int) bool { return rowCounts[i] < rowCounts[j] })
	require.Equal(t, []int64{0, 8}, rowCounts)

	var memberCount int64
	require.NoError(t, db.Model(&APIRequestLogExportMember{}).Count(&memberCount).Error)
	require.Equal(t, int64(8), memberCount)
	var distinctTurnCount int64
	require.NoError(t, db.Model(&APIRequestLogExportMember{}).Distinct("turn_record_id").Count(&distinctTurnCount).Error)
	require.Equal(t, memberCount, distinctTurnCount)
}

func TestAPIRequestLogExportExcludesCompletedTurnsWithoutItems(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	empty := createAPIRequestLogExportTestTurn(t, db, "empty-session", "empty-turn", APIRequestLogTurnStatusCompleted, APIRequestLogTurnAttributionExact, 100)
	require.NoError(t, db.Model(&empty).Update("item_count", 0).Error)
	eligible := createAPIRequestLogExportTestTurn(t, db, "eligible-session", "eligible-turn", APIRequestLogTurnStatusCompleted, APIRequestLogTurnAttributionExact, 101)

	preview, err := PreviewAPIRequestLogExport(db, APIRequestLogTurnQueryParams{ModelName: "gpt-export"}, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), preview.MatchedCount)
	require.Equal(t, int64(1), preview.AvailableCount)

	batch, err := CreateAPIRequestLogExportBatch(db, APIRequestLogTurnQueryParams{ModelName: "gpt-export"}, false)
	require.NoError(t, err)
	require.Equal(t, eligible.Id, batch.CutoffTurnId)
	require.Equal(t, int64(1), batch.RowCount)
	var member APIRequestLogExportMember
	require.NoError(t, db.Where("batch_id = ?", batch.Id).First(&member).Error)
	require.Equal(t, eligible.Id, member.TurnRecordId)
}

func TestAPIRequestLogExportTurnRowLockStrengthByDialect(t *testing.T) {
	require.Empty(t, apiRequestLogExportTurnLockStrength("postgres"), "PostgreSQL coordinates through advisory locks")
	require.Equal(t, "UPDATE", apiRequestLogExportTurnLockStrength("mysql"), "MySQL 5.7 does not support FOR SHARE")
	require.Empty(t, apiRequestLogExportTurnLockStrength("sqlite"), "SQLite batch creation already serializes the write transaction")
}

func TestAPIRequestLogExportBatchLeaseTakeoverAndRetryPreserveMembers(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	createAPIRequestLogExportTestTurn(t, db, "lease-session", "lease-turn", APIRequestLogTurnStatusCompleted, APIRequestLogTurnAttributionExact, 100)
	batch, err := CreateAPIRequestLogExportBatch(db, APIRequestLogTurnQueryParams{ModelName: "gpt-export"}, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), batch.RowCount)

	claimedA, err := ClaimAPIRequestLogExportBatch(db, batch.Tag, "worker-a", time.Minute)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogExportBatchStatusBuilding, claimedA.Status)
	require.Equal(t, "worker-a", claimedA.BuildOwner)
	require.Equal(t, 1, claimedA.BuildAttempt)

	_, err = ClaimAPIRequestLogExportBatch(db, batch.Tag, "worker-b", time.Minute)
	require.ErrorIs(t, err, ErrAPIRequestLogExportBatchNotClaimable)
	_, err = RetryAPIRequestLogExportBatchPending(db, batch.Tag)
	require.ErrorIs(t, err, ErrAPIRequestLogExportBatchNotClaimable)

	require.NoError(t, db.Model(&APIRequestLogExportBatch{}).Where("id = ?", batch.Id).Update("lease_expires_at", time.Now().UTC().Unix()-1).Error)
	claimedB, err := ClaimAPIRequestLogExportBatch(db, batch.Tag, "worker-b", time.Minute)
	require.NoError(t, err)
	require.Equal(t, "worker-b", claimedB.BuildOwner)
	require.Equal(t, 2, claimedB.BuildAttempt)

	_, err = RenewAPIRequestLogExportBatchLease(db, batch.Tag, "worker-a", time.Minute)
	require.ErrorIs(t, err, ErrAPIRequestLogExportBatchLeaseLost)
	_, err = MarkAPIRequestLogExportBatchCompleted(db, batch.Tag, "worker-a", "/tmp/stale.jsonl", strings.Repeat("a", 64), batch.RowCount)
	require.ErrorIs(t, err, ErrAPIRequestLogExportBatchLeaseLost)
	_, err = MarkAPIRequestLogExportBatchFailed(db, batch.Tag, "worker-a", errors.New("stale worker"))
	require.ErrorIs(t, err, ErrAPIRequestLogExportBatchLeaseLost)

	var before []APIRequestLogExportMember
	require.NoError(t, db.Where("batch_id = ?", batch.Id).Order("sequence ASC").Find(&before).Error)
	failed, err := MarkAPIRequestLogExportBatchFailed(db, batch.Tag, "worker-b", errors.New("disk full"))
	require.NoError(t, err)
	require.Equal(t, APIRequestLogExportBatchStatusFailed, failed.Status)
	retried, err := RetryAPIRequestLogExportBatchPending(db, batch.Tag)
	require.NoError(t, err)
	require.Equal(t, APIRequestLogExportBatchStatusPending, retried.Status)
	require.Equal(t, batch.RowCount, retried.RowCount)
	require.Equal(t, batch.CutoffTurnId, retried.CutoffTurnId)
	var after []APIRequestLogExportMember
	require.NoError(t, db.Where("batch_id = ?", batch.Id).Order("sequence ASC").Find(&after).Error)
	require.Equal(t, before, after)
}

func TestAPIRequestLogExportClaimFreezesTurnAgainstLateMaterialization(t *testing.T) {
	db := setupAPIRequestLogTurnTestDB(t)
	firstLog, firstItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, TokenId: 2, ModelName: "gpt-export", CreatedAt: 500, PromptTokens: 10,
	}, []APIRequestLogItem{{
		Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: "exported content",
	}})
	turn, err := MaterializeAPIRequestLogTurn(db, firstLog, APIRequestLogTurnMeta{
		SessionId: "frozen-session", TurnId: "frozen-turn", Protocol: "codex", StartedAt: 500, CompletedAt: 500,
		CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionExact,
	}, firstItems)
	require.NoError(t, err)

	batch, err := CreateAPIRequestLogExportBatch(db, APIRequestLogTurnQueryParams{SessionId: "frozen-session"}, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), batch.RowCount)
	require.Equal(t, turn.Id, batch.CutoffTurnId)

	lateLog, lateItems := createAPIRequestLogTurnTestRequest(t, db, APIRequestLog{
		UserId: 1, TokenId: 2, ModelName: "gpt-export", CreatedAt: 600, PromptTokens: 99,
	}, []APIRequestLogItem{{
		Seq: 1, Phase: APIRequestLogPhaseOutput, ItemType: APIRequestLogItemMessage, Role: "assistant", ContentType: "text", Content: "late content",
	}})
	frozen, err := MaterializeAPIRequestLogTurn(db, lateLog, APIRequestLogTurnMeta{
		SessionId: "frozen-session", TurnId: "frozen-turn", Protocol: "codex", StartedAt: 500, CompletedAt: 600,
		CompletionStatus: APIRequestLogTurnStatusCompleted, Attribution: APIRequestLogTurnAttributionExact,
	}, lateItems)
	require.NoError(t, err)
	require.Equal(t, 1, frozen.RequestCount)
	require.Equal(t, 1, frozen.ItemCount)
	require.Equal(t, 10, frozen.PromptTokens)

	page, err := GetAPIRequestLogExportBatchTurnPage(db, batch.Id, 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Len(t, page.Items[0].Turn.Requests, 1)
	require.Len(t, page.Items[0].Turn.Items, 1)
	require.Equal(t, APIRequestLogBody("exported content"), page.Items[0].Turn.Items[0].Content)
}
