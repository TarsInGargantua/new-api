package model

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func TestDailyBalanceDisplayUnit(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	old := *settings
	t.Cleanup(func() { *settings = old })
	for _, unit := range []string{"USD", "CNY", "CUSTOM", "TOKENS"} {
		t.Run(unit, func(t *testing.T) {
			settings.QuotaDisplayType = unit
			settings.CustomCurrencyExchangeRate = 2
			quota, err := DailyBalanceQuota()
			require.NoError(t, err)
			if unit == "TOKENS" {
				require.Equal(t, 100, quota)
			} else {
				rate := operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate)
				require.Equal(t, int(math.Round(100*common.QuotaPerUnit/rate)), quota)
			}
		})
	}
}

func TestBalanceResetMidnightAndIdempotence(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	before := time.Date(2026, 9, 14, 15, 59, 59, 0, time.UTC)
	after := before.Add(time.Second) // Beijing midnight, not UTC midnight.
	quota, err := DailyBalanceQuota()
	require.NoError(t, err)
	users := []User{
		{Username: "alice", Quota: quota + 1000, UsedQuota: 700, RequestCount: 9},
		{Username: "admin", Quota: 12345},
		{Username: "bob", Quota: -20, Role: common.RoleAdminUser},
	}
	for i := range users {
		users[i].AffCode = users[i].Username
	}
	require.NoError(t, DB.Create(&users).Error)
	require.NoError(t, ResetDailyBalances(before))
	for _, index := range []int{0, 2} {
		var user User
		require.NoError(t, DB.First(&user, users[index].Id).Error)
		require.Equal(t, quota, user.Quota)
	}
	require.NoError(t, changeUserBalance(users[0].Id, -80, before))
	// Another process/startup must not replenish the same day's spending.
	require.NoError(t, ResetDailyBalances(before))
	var alice User
	require.NoError(t, DB.First(&alice, users[0].Id).Error)
	require.Equal(t, quota-80, alice.Quota)
	require.NoError(t, changeUserBalance(users[0].Id, -30, after))
	require.NoError(t, ResetDailyBalances(after))
	require.NoError(t, DB.First(&alice, users[0].Id).Error)
	require.Equal(t, quota-30, alice.Quota)
	require.Equal(t, 700, alice.UsedQuota)
	require.Equal(t, 9, alice.RequestCount)
	require.Equal(t, after.Unix(), alice.BalanceResetAt)
	var admin User
	require.NoError(t, DB.First(&admin, users[1].Id).Error)
	require.Equal(t, 12345, admin.Quota)
	require.Zero(t, admin.BalanceResetAt)
}

func TestBalanceResetUsernameAndManualRepeat(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	now := time.Now()
	users := []User{{Username: "ryan", Quota: 12}, {Username: "other", Quota: 13}, {Username: "ADMIN", Quota: 14}}
	for i := range users {
		users[i].AffCode = users[i].Username
	}
	require.NoError(t, DB.Create(&users).Error)
	_, err := ResetOwnBalance(users[0].Id, "other", now)
	require.ErrorIs(t, err, ErrBalanceResetUsername)
	_, err = ResetOwnBalance(users[0].Id, "RYAN", now)
	require.ErrorIs(t, err, ErrBalanceResetUsername)
	_, err = ResetOwnBalance(users[2].Id, "ADMIN", now)
	require.ErrorIs(t, err, ErrBalanceResetAdmin)
	quota, err := ResetOwnBalance(users[0].Id, "ryan", now)
	require.NoError(t, err)
	require.NoError(t, changeUserBalance(users[0].Id, -123, now))
	_, err = ResetOwnBalance(users[0].Id, "ryan", now)
	require.NoError(t, err)
	require.NoError(t, ResetDailyBalances(now))
	var user User
	require.NoError(t, DB.First(&user, users[0].Id).Error)
	require.Equal(t, quota, user.Quota)
	var admin User
	require.NoError(t, DB.First(&admin, users[2].Id).Error)
	require.Equal(t, 14, admin.Quota)
}

func TestBalanceResetConcurrentSpendingAndBatchMode(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	sqlDB, err := DB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	oldBatch := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = true
	t.Cleanup(func() { common.BatchUpdateEnabled = oldBatch })
	user := User{Username: "alice", Quota: 1}
	require.NoError(t, DB.Create(&user).Error)
	var wg sync.WaitGroup
	errors := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); errors <- DecreaseUserQuota(user.Id, 1, false) }()
		go func() { defer wg.Done(); errors <- ResetDailyBalances(time.Now()) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	target, err := DailyBalanceQuota()
	require.NoError(t, err)
	actual, err := GetUserQuota(user.Id, false)
	require.NoError(t, err)
	require.Equal(t, target-20, actual)
	batchUpdateLocks[BatchUpdateTypeUserQuota].Lock()
	defer batchUpdateLocks[BatchUpdateTypeUserQuota].Unlock()
	require.Zero(t, batchUpdateStores[BatchUpdateTypeUserQuota][user.Id])
}

func TestBalanceResetSurvivesStaleProfileUpdate(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	t.Cleanup(func() { common.RedisEnabled = oldRedis })
	user := User{Username: "alice", Quota: 123, BalanceResetAt: BalanceResetDay(time.Now().Add(-24 * time.Hour))}
	require.NoError(t, DB.Create(&user).Error)
	quota, err := ResetOwnBalance(user.Id, "alice", time.Now())
	require.NoError(t, err)
	user.DisplayName = "Updated profile"
	require.NoError(t, user.Update(false))
	var updated User
	require.NoError(t, DB.First(&updated, user.Id).Error)
	require.Equal(t, "Updated profile", updated.DisplayName)
	require.Equal(t, quota, updated.Quota)
	require.Equal(t, BalanceResetDay(time.Now()), updated.BalanceResetAt)
}

func TestDailyBalanceNewAccountInitialization(t *testing.T) {
	user := User{Username: "alice"}
	require.NoError(t, user.initializeDailyBalance())
	quota, err := DailyBalanceQuota()
	require.NoError(t, err)
	require.Equal(t, quota, user.Quota)
	require.Equal(t, BalanceResetDay(time.Now()), user.BalanceResetAt)
	admin := User{Username: "admin"}
	require.NoError(t, admin.initializeDailyBalance())
	require.Equal(t, common.QuotaForNewUser, admin.Quota)
	require.Zero(t, admin.BalanceResetAt)
}
