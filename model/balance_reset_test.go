package model

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNormalizeBalancesOncePreservesExcludedRolesAndSpending(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	require.NoError(t, DB.AutoMigrate(&Option{}))
	users := []User{
		{Role: common.RoleCommonUser, Username: "ryan", AffCode: "ryan", Quota: 123, UsedQuota: 456, RequestCount: 7},
		{Role: common.RoleAdminUser, Username: "staff", AffCode: "staff", Quota: -10},
		{Role: common.RoleGuestUser, Username: "admin", AffCode: "admin", Quota: 999},
		{Role: common.RoleTestUser, Username: "ADMIN", AffCode: "ADMIN", Quota: 888},
	}
	require.NoError(t, DB.Create(&users).Error)
	now := time.Now()
	target, err := DailyBalanceQuota()
	require.NoError(t, err)
	require.NoError(t, NormalizeBalancesSeptember2026(now))
	for i := range users {
		var actual User
		require.NoError(t, DB.First(&actual, users[i].Id).Error)
		if i < 2 {
			require.Equal(t, target, actual.Quota)
			require.Equal(t, BalanceResetDay(now), actual.BalanceResetAt)
		} else {
			require.Equal(t, users[i].Quota, actual.Quota)
		}
		require.Equal(t, users[i].UsedQuota, actual.UsedQuota)
		require.Equal(t, users[i].RequestCount, actual.RequestCount)
	}
	require.NoError(t, DecreaseUserQuota(users[0].Id, 1234, false))
	require.NoError(t, NormalizeBalancesSeptember2026(now))
	var ryan User
	require.NoError(t, DB.First(&ryan, users[0].Id).Error)
	require.Equal(t, target-1234, ryan.Quota)
}

func TestNormalizeBalancesFailureRollsBackClaim(t *testing.T) {
	setupAPIRequestLogTestDB(t)
	require.NoError(t, DB.AutoMigrate(&Option{}))
	user := User{Role: common.RoleCommonUser, Username: "ryan", Quota: 12}
	require.NoError(t, DB.Create(&user).Error)
	callback := "test:fail_balance_normalization"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			tx.AddError(errors.New("simulated balance write failure"))
		}
	}))
	err := NormalizeBalancesSeptember2026(time.Now())
	require.NoError(t, DB.Callback().Update().Remove(callback))
	require.Error(t, err)
	var count int64
	require.NoError(t, DB.Model(&Option{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, DB.First(&user, user.Id).Error)
	require.Equal(t, 12, user.Quota)
	require.NoError(t, NormalizeBalancesSeptember2026(time.Now()))
	require.NoError(t, DB.First(&user, user.Id).Error)
	target, err := DailyBalanceQuota()
	require.NoError(t, err)
	require.Equal(t, target, user.Quota)
}

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
		{Role: common.RoleCommonUser, Username: "alice", Quota: quota + 1000, UsedQuota: 700, RequestCount: 9},
		{Role: common.RoleGuestUser, Username: "admin", Quota: 12345},
		{Role: common.RoleAdminUser, Username: "bob", Quota: -20},
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
	users := []User{{Role: common.RoleCommonUser, Username: "ryan", Quota: 12}, {Role: common.RoleCommonUser, Username: "other", Quota: 13}, {Role: common.RoleTestUser, Username: "ADMIN", Quota: 14}}
	for i := range users {
		users[i].AffCode = users[i].Username
	}
	require.NoError(t, DB.Create(&users).Error)
	_, err := ResetOwnBalance(users[0].Id, "other", now)
	require.ErrorIs(t, err, ErrBalanceResetUsername)
	_, err = ResetOwnBalance(users[0].Id, "RYAN", now)
	require.ErrorIs(t, err, ErrBalanceResetUsername)
	_, err = ResetOwnBalance(users[2].Id, "ADMIN", now)
	require.ErrorIs(t, err, ErrBalanceResetRole)
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
	user := User{Role: common.RoleCommonUser, Username: "alice", Quota: 1}
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
	user := User{Role: common.RoleCommonUser, Username: "alice", Quota: 123, BalanceResetAt: BalanceResetDay(time.Now().Add(-24 * time.Hour))}
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
	user := User{Role: common.RoleCommonUser, Username: "alice"}
	require.NoError(t, user.initializeDailyBalance())
	quota, err := DailyBalanceQuota()
	require.NoError(t, err)
	require.Equal(t, quota, user.Quota)
	require.Equal(t, BalanceResetDay(time.Now()), user.BalanceResetAt)
	admin := User{Role: common.RoleGuestUser, Username: "admin"}
	require.NoError(t, admin.initializeDailyBalance())
	require.Zero(t, admin.Quota)
	require.Zero(t, admin.BalanceResetAt)
}
