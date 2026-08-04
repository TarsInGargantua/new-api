package model

import (
	"context"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/require"
)

func TestDeferredAPIRequestLogMaterializationPersistsRawDataBeforeTurn(t *testing.T) {
	db := setupAPIRequestLogTestDB(t)
	require.NoError(t, EnsureAPIRequestLogMaterializedTables(db))

	oldDeferred := common.APIRequestLogDeferredMaterialization
	common.APIRequestLogDeferredMaterialization = true
	t.Cleanup(func() {
		common.APIRequestLogDeferredMaterialization = oldDeferred
	})

	log := &APIRequestLog{
		Source: APIRequestLogSourceLive, Username: "deferred-user", ModelName: "gpt-deferred",
		CreatedAt: 100, RequestId: "request-deferred", APIFormat: "responses",
		Items: []APIRequestLogItem{{Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", ContentType: "text", Content: APIRequestLogBody("hello")}},
		TurnMeta: &APIRequestLogTurnMeta{
			SessionId: "session-deferred", TurnId: "turn-deferred", Protocol: "responses",
			StartedAt: 100, CompletionStatus: APIRequestLogTurnStatusCompleted,
		},
	}
	require.NoError(t, CreateAPIRequestLog(log))
	require.Positive(t, log.Id)

	var stored APIRequestLog
	require.NoError(t, db.First(&stored, log.Id).Error)
	require.Equal(t, APIRequestLogItemsOK, stored.ItemsStatus)
	var itemCount int64
	require.NoError(t, db.Model(&APIRequestLogItem{}).Where("log_id = ?", log.Id).Count(&itemCount).Error)
	require.EqualValues(t, 1, itemCount)
	var turnCount int64
	require.NoError(t, db.Model(&APIRequestLogTurn{}).Count(&turnCount).Error)
	require.Zero(t, turnCount)

	job, err := ClaimAPIRequestLogMaterializationJob(context.Background(), db, APIRequestLogMaterializationWorkerOptions{
		Now: func() time.Time { return time.Unix(200, 0) },
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, log.Id, job.LogId)
	require.NoError(t, ProcessAPIRequestLogMaterializationJob(context.Background(), db, job, func() time.Time { return time.Unix(200, 0) }))

	var completed APIRequestLogMaterializationJob
	require.NoError(t, db.First(&completed, job.Id).Error)
	require.Equal(t, APIRequestLogMaterializationOK, completed.Status)
	var turn APIRequestLogTurn
	require.NoError(t, db.Where("session_id = ? AND turn_id = ?", "session-deferred", "turn-deferred").First(&turn).Error)
	require.Equal(t, 1, turn.RequestCount)
	require.Equal(t, 1, turn.ItemCount)
}

func TestDeferredAPIRequestLogMaterializationReclaimsExpiredLease(t *testing.T) {
	db := setupAPIRequestLogTestDB(t)
	require.NoError(t, EnsureAPIRequestLogMaterializationJobTable(db))
	require.NoError(t, db.Create(&APIRequestLogMaterializationJob{
		LogId: 1, Status: APIRequestLogMaterializationProcessing, LockedAt: 100, LeaseVersion: 2,
	}).Error)

	job, err := ClaimAPIRequestLogMaterializationJob(context.Background(), db, APIRequestLogMaterializationWorkerOptions{
		Lease: time.Minute,
		Now:   func() time.Time { return time.Unix(200, 0) },
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, 3, job.LeaseVersion)
	require.Equal(t, 1, job.Attempts)
}

func TestDeferredAPIRequestLogMaterializationFailureSchedulesRetry(t *testing.T) {
	db := setupAPIRequestLogTestDB(t)
	require.NoError(t, EnsureAPIRequestLogMaterializationJobTable(db))
	require.NoError(t, db.Create(&APIRequestLogMaterializationJob{
		LogId: 999, Status: APIRequestLogMaterializationPending,
	}).Error)

	now := time.Unix(300, 0)
	job, err := ClaimAPIRequestLogMaterializationJob(context.Background(), db, APIRequestLogMaterializationWorkerOptions{
		Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	require.NotNil(t, job)
	err = ProcessAPIRequestLogMaterializationJob(context.Background(), db, job, func() time.Time { return now })
	require.Error(t, err)
	require.Contains(t, err.Error(), "record not found")

	var retry APIRequestLogMaterializationJob
	require.NoError(t, db.First(&retry, job.Id).Error)
	require.Equal(t, APIRequestLogMaterializationRetry, retry.Status)
	require.Equal(t, now.Add(time.Second).Unix(), retry.NextAttemptAt)
	require.NotEmpty(t, retry.LastError)
}

func TestScheduleAPIRequestLogMaterializationBacklogOnlyQueuesRecoverableParents(t *testing.T) {
	db := setupAPIRequestLogTestDB(t)
	require.NoError(t, EnsureAPIRequestLogMaterializedTables(db))

	first := APIRequestLog{APIFormat: "responses", CreatedAt: 100, ItemsStatus: APIRequestLogItemsPending}
	second := APIRequestLog{APIFormat: "openai_responses", CreatedAt: 101, ItemsStatus: APIRequestLogItemsFailed, ItemsError: "materialization timed out"}
	parentOnly := APIRequestLog{APIFormat: "responses", CreatedAt: 102, ItemsStatus: APIRequestLogItemsPending}
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, db.Create(&second).Error)
	require.NoError(t, db.Create(&parentOnly).Error)
	require.NoError(t, db.Create(&[]APIRequestLogItem{
		{LogId: first.Id, Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", Content: "first"},
		{LogId: second.Id, Seq: 1, Phase: APIRequestLogPhaseInput, ItemType: APIRequestLogItemMessage, Role: "user", Content: "second"},
	}).Error)
	status, err := GetAPIRequestLogMaterializationBacklogStatus(t.Context(), db)
	require.NoError(t, err)
	require.Equal(t, int64(2), status.Recoverable)
	require.Equal(t, int64(1), status.ParentOnly)

	backfill, err := ScheduleAPIRequestLogMaterializationBacklog(t.Context(), db, 1)
	require.NoError(t, err)
	require.Equal(t, 1, backfill.Candidates)
	require.Equal(t, int64(1), backfill.Scheduled)
	require.True(t, backfill.HasMore)

	backfill, err = ScheduleAPIRequestLogMaterializationBacklog(t.Context(), db, 10)
	require.NoError(t, err)
	require.Equal(t, 1, backfill.Candidates)
	require.Equal(t, int64(1), backfill.Scheduled)
	require.False(t, backfill.HasMore)

	var jobs []APIRequestLogMaterializationJob
	require.NoError(t, db.Order("log_id ASC").Find(&jobs).Error)
	require.Len(t, jobs, 2)
	var meta APIRequestLogTurnMeta
	require.NoError(t, common.Unmarshal([]byte(jobs[0].TurnMeta), &meta))
	require.Equal(t, "responses", meta.Protocol)

	var stored []APIRequestLog
	require.NoError(t, db.Where("id IN ?", []int{first.Id, second.Id, parentOnly.Id}).Order("id ASC").Find(&stored).Error)
	require.Equal(t, APIRequestLogItemsOK, stored[0].ItemsStatus)
	require.Equal(t, APIRequestLogItemsOK, stored[1].ItemsStatus)
	require.Equal(t, APIRequestLogItemsPending, stored[2].ItemsStatus)
}

func TestEnqueueAPIRequestLogMaterializationRefreshesLeaseWithoutReadLock(t *testing.T) {
	db := setupAPIRequestLogTestDB(t)
	require.NoError(t, EnsureAPIRequestLogMaterializationJobTable(db))
	log := APIRequestLog{Id: 42, APIFormat: "responses", TurnMeta: &APIRequestLogTurnMeta{SessionId: "session", TurnId: "turn"}}
	require.NoError(t, db.Create(&APIRequestLogMaterializationJob{
		LogId: log.Id, Status: APIRequestLogMaterializationProcessing, LockedAt: 100, LeaseVersion: 3,
	}).Error)

	require.NoError(t, enqueueAPIRequestLogMaterialization(db, &log))
	var job APIRequestLogMaterializationJob
	require.NoError(t, db.Where("log_id = ?", log.Id).First(&job).Error)
	require.Equal(t, APIRequestLogMaterializationPending, job.Status)
	require.Zero(t, job.LockedAt)
	require.Equal(t, 4, job.LeaseVersion)
}
