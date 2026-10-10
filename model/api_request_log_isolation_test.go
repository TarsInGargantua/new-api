package model

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRequestLogRemoteInitializationDoesNotDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	var accepted atomic.Int32
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted.Add(1)
			conn.Close()
		}
	}()
	t.Setenv("REQUEST_LOG_SQL_DSN", "test:test@tcp("+listener.Addr().String()+")/backup")
	start := time.Now()
	db, err := chooseDedicatedRequestLogDB()
	require.NoError(t, err)
	defer closeDB(db)
	require.Less(t, time.Since(start), time.Second)
	require.Zero(t, accepted.Load(), "gateway initialization must not wait on a backup handshake")
	pool, err := db.DB()
	require.NoError(t, err)
	require.Zero(t, pool.Stats().OpenConnections)
}

func TestRequestLogUnavailableDestinationDoesNotTouchQueueAndRecovers(t *testing.T) {
	local := setupAPIRequestLogTestDB(t)
	oldEnabled := common.APIRequestLogOutboxEnabled
	common.APIRequestLogOutboxEnabled = true
	t.Cleanup(func() { common.APIRequestLogOutboxEnabled = oldEnabled })
	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{RequestId: "durable-before-outage"}))
	remote, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "remote.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, remote.AutoMigrate(&APIRequestLog{}, &APIRequestLogItem{}))
	REQUEST_LOG_DB = remote
	require.NoError(t, closeDB(remote))

	runAPIRequestLogOutboxWorkerBatch()
	require.True(t, APIRequestLogCapturePaused())
	require.Greater(t, atomic.LoadInt64(&apiRequestLogOutboxRetryAt), time.Now().UnixNano())
	var job APIRequestLogOutbox
	require.NoError(t, local.First(&job).Error)
	require.Zero(t, job.Attempts, "remote outage must not churn local row locks")
	require.Zero(t, job.LeaseUntil)
	require.ErrorIs(t, CreateAPIRequestLog(&APIRequestLog{RequestId: "during-outage"}), ErrAPIRequestLogCapturePaused)
	var count int64
	require.NoError(t, local.Model(&APIRequestLogOutbox{}).Count(&count).Error)
	require.EqualValues(t, 1, count, "outage must not grow the primary disk backlog")
	before := apiRequestLogOutboxFailures
	runAPIRequestLogOutboxWorkerBatch()
	require.Equal(t, before, apiRequestLogOutboxFailures, "workers share the same retry cooldown")

	remote, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "recovered.db")), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { closeDB(remote) })
	require.NoError(t, remote.AutoMigrate(&APIRequestLog{}, &APIRequestLogItem{}))
	REQUEST_LOG_DB = remote
	atomic.StoreInt64(&apiRequestLogOutboxRetryAt, 0)
	runAPIRequestLogOutboxWorkerBatch()
	require.False(t, APIRequestLogCapturePaused())
	require.NoError(t, remote.Model(&APIRequestLog{}).Where("request_id = ?", "durable-before-outage").Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, local.Model(&APIRequestLogOutbox{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, CreateAPIRequestLog(&APIRequestLog{RequestId: "after-recovery"}))
}

func TestRequestLogSaturatedEnqueuePoolDoesNotConsumeBusinessConnections(t *testing.T) {
	primary := setupAPIRequestLogTestDB(t)
	oldPath := common.SQLitePath
	oldEnabled := common.APIRequestLogOutboxEnabled
	oldEnqueue, oldDelivery := apiRequestLogEnqueueDB, apiRequestLogDeliveryDB
	common.SQLitePath = primary.Dialector.(*sqlite.Dialector).DSN
	common.APIRequestLogOutboxEnabled = true
	t.Setenv("SQL_DSN", "local")
	require.NoError(t, initAPIRequestLogOutboxPools())
	t.Cleanup(func() {
		closeDB(apiRequestLogEnqueueDB)
		closeDB(apiRequestLogDeliveryDB)
		apiRequestLogEnqueueDB, apiRequestLogDeliveryDB = oldEnqueue, oldDelivery
		common.SQLitePath = oldPath
		common.APIRequestLogOutboxEnabled = oldEnabled
	})
	require.NoError(t, EnsureAPIRequestLogOutboxTable())
	pool, err := apiRequestLogEnqueueDB.DB()
	require.NoError(t, err)
	require.Equal(t, 2, pool.Stats().MaxOpenConnections)
	workerPool, err := apiRequestLogDeliveryDB.DB()
	require.NoError(t, err)
	require.Equal(t, 1, workerPool.Stats().MaxOpenConnections)
	conn1, err := pool.Conn(context.Background())
	require.NoError(t, err)
	defer conn1.Close()
	conn2, err := pool.Conn(context.Background())
	require.NoError(t, err)
	defer conn2.Close()
	start := time.Now()
	err = CreateAPIRequestLog(&APIRequestLog{RequestId: "queue-full"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var one int
	require.NoError(t, primary.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error)
	require.Equal(t, 1, one)
}

func TestRequestLogExpiredLeaseCannotAcknowledgeAnotherWorker(t *testing.T) {
	db := setupAPIRequestLogTestDB(t)
	require.NoError(t, EnsureAPIRequestLogOutboxTable())
	job := APIRequestLogOutbox{AvailableAt: 1, Payload: `{"log":{"request_id":"lease-fence"}}`}
	require.NoError(t, db.Create(&job).Error)
	jobs, err := claimAPIRequestLogOutboxBatch(db, 100, 1)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.NoError(t, db.Model(&job).Update("attempts", jobs[0].Attempts+1).Error)
	require.NoError(t, syncAPIRequestLogOutboxJob(db, &jobs[0]))
	var remaining int64
	require.NoError(t, db.Model(&APIRequestLogOutbox{}).Where("id = ?", job.Id).Count(&remaining).Error)
	require.EqualValues(t, 1, remaining, "stale worker must retain a re-leased job")
}
