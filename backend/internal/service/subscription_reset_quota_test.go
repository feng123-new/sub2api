//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// resetQuotaUserSubRepoStub 支持 GetByID、ResetUsageWindows，
// 其余方法继承 userSubRepoNoop（panic）。
type resetQuotaUserSubRepoStub struct {
	userSubRepoNoop

	sub *UserSubscription

	resetDailyCalled   bool
	resetWeeklyCalled  bool
	resetMonthlyCalled bool
	resetDailyErr      error
	resetWeeklyErr     error
	resetMonthlyErr    error
	dailyStart         time.Time
	periodicStart      time.Time
}

func (r *resetQuotaUserSubRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func (r *resetQuotaUserSubRepoStub) ResetUsageWindows(_ context.Context, _ int64, resetDaily, resetWeekly, resetMonthly bool, dailyStart, periodicStart time.Time) error {
	r.resetDailyCalled = resetDaily
	r.resetWeeklyCalled = resetWeekly
	r.resetMonthlyCalled = resetMonthly
	r.dailyStart = dailyStart
	r.periodicStart = periodicStart
	if resetDaily && r.resetDailyErr != nil {
		return r.resetDailyErr
	}
	if resetWeekly && r.resetWeeklyErr != nil {
		return r.resetWeeklyErr
	}
	if resetMonthly && r.resetMonthlyErr != nil {
		return r.resetMonthlyErr
	}
	if r.sub == nil {
		return nil
	}
	if resetDaily {
		r.sub.DailyUsageUSD = 0
		r.sub.DailyWindowStart = &dailyStart
	}
	if resetWeekly {
		r.sub.WeeklyUsageUSD = 0
		r.sub.WeeklyWindowStart = &periodicStart
	}
	if resetMonthly {
		r.sub.MonthlyUsageUSD = 0
		r.sub.MonthlyWindowStart = &periodicStart
	}
	return nil
}

func (r *resetQuotaUserSubRepoStub) ResetDailyUsage(_ context.Context, _ int64, _ *time.Time, windowStart time.Time) error {
	r.resetDailyCalled = true
	if r.resetDailyErr == nil && r.sub != nil {
		r.sub.DailyUsageUSD = 0
		r.sub.DailyWindowStart = &windowStart
	}
	return r.resetDailyErr
}

func (r *resetQuotaUserSubRepoStub) ResetWeeklyUsage(_ context.Context, _ int64, _ *time.Time, _ time.Time) error {
	r.resetWeeklyCalled = true
	return r.resetWeeklyErr
}

func (r *resetQuotaUserSubRepoStub) ResetMonthlyUsage(_ context.Context, _ int64, _ *time.Time, _ time.Time) error {
	r.resetMonthlyCalled = true
	return r.resetMonthlyErr
}

func newResetQuotaSvc(stub *resetQuotaUserSubRepoStub) *SubscriptionService {
	return NewSubscriptionService(groupRepoNoop{}, stub, nil, nil, nil)
}

func TestAdminResetQuota_ResetBoth(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 1, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)
	resetAt := time.Date(2026, 7, 1, 10, 37, 42, 123, time.UTC)
	svc.now = func() time.Time { return resetAt }

	result, err := svc.AdminResetQuota(context.Background(), 1, true, true, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, stub.resetDailyCalled, "应调用 ResetDailyUsage")
	require.True(t, stub.resetWeeklyCalled, "应调用 ResetWeeklyUsage")
	require.False(t, stub.resetMonthlyCalled, "不应调用 ResetMonthlyUsage")
	// 手动重置后日窗口锚定当天 0 点（保持 0 点刷新节奏），周窗口锚定重置时刻。
	require.Equal(t, timezone.StartOfDay(resetAt), stub.dailyStart)
	require.Equal(t, resetAt, stub.periodicStart)
	require.Equal(t, timezone.StartOfDay(resetAt), *result.DailyWindowStart)
	require.Equal(t, resetAt, *result.WeeklyWindowStart)
}

func TestAdminResetQuota_ResetDailyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 2, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 2, true, false, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, stub.resetDailyCalled, "应调用 ResetDailyUsage")
	require.False(t, stub.resetWeeklyCalled, "不应调用 ResetWeeklyUsage")
	require.False(t, stub.resetMonthlyCalled, "不应调用 ResetMonthlyUsage")
}

func TestAdminResetQuota_ResetWeeklyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 3, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 3, false, true, false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, stub.resetDailyCalled, "不应调用 ResetDailyUsage")
	require.True(t, stub.resetWeeklyCalled, "应调用 ResetWeeklyUsage")
	require.False(t, stub.resetMonthlyCalled, "不应调用 ResetMonthlyUsage")
}

func TestAdminResetQuota_BothFalseReturnsError(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 7, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 7, false, false, false)

	require.ErrorIs(t, err, ErrInvalidInput)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}

func TestAdminResetQuota_SubscriptionNotFound(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{sub: nil}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 999, true, true, true)

	require.ErrorIs(t, err, ErrSubscriptionNotFound)
	require.False(t, stub.resetDailyCalled)
	require.False(t, stub.resetWeeklyCalled)
	require.False(t, stub.resetMonthlyCalled)
}

func TestAdminResetQuota_ResetDailyUsageError(t *testing.T) {
	dbErr := errors.New("db error")
	stub := &resetQuotaUserSubRepoStub{
		sub:           &UserSubscription{ID: 4, UserID: 10, GroupID: 20},
		resetDailyErr: dbErr,
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 4, true, true, false)

	require.ErrorIs(t, err, dbErr)
	require.True(t, stub.resetDailyCalled)
	require.True(t, stub.resetWeeklyCalled, "原子重置应在一次调用中提交所选窗口")
}

func TestAdminResetQuota_ResetWeeklyUsageError(t *testing.T) {
	dbErr := errors.New("db error")
	stub := &resetQuotaUserSubRepoStub{
		sub:            &UserSubscription{ID: 5, UserID: 10, GroupID: 20},
		resetWeeklyErr: dbErr,
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 5, false, true, false)

	require.ErrorIs(t, err, dbErr)
	require.True(t, stub.resetWeeklyCalled)
}

func TestAdminResetQuota_ResetMonthlyOnly(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{ID: 8, UserID: 10, GroupID: 20},
	}
	svc := newResetQuotaSvc(stub)

	result, err := svc.AdminResetQuota(context.Background(), 8, false, false, true)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, stub.resetDailyCalled, "不应调用 ResetDailyUsage")
	require.False(t, stub.resetWeeklyCalled, "不应调用 ResetWeeklyUsage")
	require.True(t, stub.resetMonthlyCalled, "应调用 ResetMonthlyUsage")
}

func TestAdminResetQuota_BeforeStartsAtSameDayPreservesAutomaticBoundary(t *testing.T) {
	startsAt := time.Date(2026, 7, 1, 15, 0, 0, 0, time.UTC)
	resetAt := time.Date(2026, 7, 1, 10, 37, 42, 123, time.UTC)
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{
			ID:        10,
			UserID:    10,
			GroupID:   20,
			StartsAt:  startsAt,
			ExpiresAt: startsAt.Add(45 * 24 * time.Hour),
		},
	}
	svc := newResetQuotaSvc(stub)
	svc.now = func() time.Time { return resetAt }

	result, err := svc.AdminResetQuota(context.Background(), 10, false, false, true)

	require.NoError(t, err)
	require.Equal(t, resetAt, *result.MonthlyWindowStart)
	boundary, ok := result.automaticWindowStartAt(result.MonthlyWindowStart, 30*24*time.Hour, resetAt.Add(30*24*time.Hour))
	require.True(t, ok)
	require.Equal(t, resetAt.Add(30*24*time.Hour), boundary)
}

func TestAdminResetQuota_ResetMonthlyUsageError(t *testing.T) {
	dbErr := errors.New("db error")
	stub := &resetQuotaUserSubRepoStub{
		sub:             &UserSubscription{ID: 9, UserID: 10, GroupID: 20},
		resetMonthlyErr: dbErr,
	}
	svc := newResetQuotaSvc(stub)

	_, err := svc.AdminResetQuota(context.Background(), 9, false, false, true)

	require.ErrorIs(t, err, dbErr)
	require.True(t, stub.resetMonthlyCalled)
}

func TestAdminResetQuota_ReturnsRefreshedSub(t *testing.T) {
	stub := &resetQuotaUserSubRepoStub{
		sub: &UserSubscription{
			ID:            6,
			UserID:        10,
			GroupID:       20,
			DailyUsageUSD: 99.9,
		},
	}

	svc := newResetQuotaSvc(stub)
	result, err := svc.AdminResetQuota(context.Background(), 6, true, false, false)

	require.NoError(t, err)
	// ResetUsageWindows stub 会将 sub.DailyUsageUSD 归零，
	// 服务应返回第二次 GetByID 的刷新值而非初始的 99.9
	require.Equal(t, float64(0), result.DailyUsageUSD, "返回的订阅应反映已归零的用量")
	require.True(t, stub.resetDailyCalled)
}

type scheduleQuotaResetRepoStub struct {
	userSubRepoNoop

	sub                *UserSubscription
	updated            bool
	updatedDaily       bool
	updatedWeekly      bool
	updatedMonthly     bool
	dailyWindowStart   time.Time
	weeklyWindowStart  time.Time
	monthlyWindowStart time.Time
}

func (r *scheduleQuotaResetRepoStub) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	if r.sub == nil || r.sub.ID != id {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func (r *scheduleQuotaResetRepoStub) ScheduleUsageWindowReset(_ context.Context, _ int64, resetDaily, resetWeekly, resetMonthly bool, dailyStart, weeklyStart, monthlyStart time.Time) error {
	r.updated = true
	r.updatedDaily = resetDaily
	r.updatedWeekly = resetWeekly
	r.updatedMonthly = resetMonthly
	r.dailyWindowStart = dailyStart
	r.weeklyWindowStart = weeklyStart
	r.monthlyWindowStart = monthlyStart
	if resetDaily {
		r.sub.DailyWindowStart = &dailyStart
	}
	if resetWeekly {
		r.sub.WeeklyWindowStart = &weeklyStart
	}
	if resetMonthly {
		r.sub.MonthlyWindowStart = &monthlyStart
	}
	return nil
}

func TestScheduleQuotaReset_SetsSelectedNextResetTimesWithoutClearingUsage(t *testing.T) {
	now := time.Date(2026, 8, 7, 10, 0, 0, 0, timezone.Location())
	resetAt := timezone.StartOfDay(now).AddDate(0, 0, 1)
	monthlyWindow := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	stub := &scheduleQuotaResetRepoStub{
		sub: &UserSubscription{
			ID:                 11,
			UserID:             22,
			GroupID:            33,
			Status:             SubscriptionStatusActive,
			StartsAt:           now.Add(-30 * 24 * time.Hour),
			ExpiresAt:          now.Add(30 * 24 * time.Hour),
			DailyUsageUSD:      12.5,
			WeeklyUsageUSD:     25,
			MonthlyUsageUSD:    50,
			MonthlyWindowStart: &monthlyWindow,
		},
	}
	svc := NewSubscriptionService(groupRepoNoop{}, stub, nil, nil, nil)
	svc.now = func() time.Time { return now }

	result, err := svc.ScheduleQuotaReset(context.Background(), 11, true, true, false, resetAt)

	require.NoError(t, err)
	require.True(t, stub.updated)
	require.True(t, stub.updatedDaily)
	require.True(t, stub.updatedWeekly)
	require.False(t, stub.updatedMonthly)
	require.Equal(t, resetAt.AddDate(0, 0, -1), stub.dailyWindowStart)
	require.Equal(t, resetAt.Add(-7*24*time.Hour), stub.weeklyWindowStart)
	require.Equal(t, resetAt.Add(-30*24*time.Hour), stub.monthlyWindowStart)
	require.Equal(t, resetAt.AddDate(0, 0, -1), *result.DailyWindowStart)
	require.Equal(t, resetAt.Add(-7*24*time.Hour), *result.WeeklyWindowStart)
	require.Equal(t, monthlyWindow, *result.MonthlyWindowStart)
	require.Equal(t, resetAt, *result.DailyResetTime())
	require.Equal(t, 12.5, result.DailyUsageUSD)
	require.Equal(t, 25.0, result.WeeklyUsageUSD)
	require.Equal(t, 50.0, result.MonthlyUsageUSD)
}

func TestScheduleQuotaReset_RejectsDailyScheduleForOneTimeQuota(t *testing.T) {
	now := time.Date(2026, 8, 7, 10, 0, 0, 0, timezone.Location())
	resetAt := timezone.StartOfDay(now).AddDate(0, 0, 1)
	stub := &scheduleQuotaResetRepoStub{
		sub: &UserSubscription{
			ID:        14,
			UserID:    22,
			GroupID:   33,
			Status:    SubscriptionStatusActive,
			StartsAt:  now,
			ExpiresAt: now.Add(23 * time.Hour),
		},
	}
	svc := NewSubscriptionService(groupRepoNoop{}, stub, nil, nil, nil)
	svc.now = func() time.Time { return now }

	_, err := svc.ScheduleQuotaReset(context.Background(), 14, true, false, false, resetAt)

	require.ErrorIs(t, err, ErrOneTimeDailyResetScheduled)
	require.False(t, stub.updated)
}

func TestScheduleQuotaReset_RejectsDailyTimeOutsideServerMidnight(t *testing.T) {
	now := time.Date(2026, 8, 7, 10, 0, 0, 0, timezone.Location())
	stub := &scheduleQuotaResetRepoStub{
		sub: &UserSubscription{
			ID:        13,
			UserID:    22,
			GroupID:   33,
			Status:    SubscriptionStatusActive,
			StartsAt:  now.Add(-24 * time.Hour),
			ExpiresAt: now.Add(48 * time.Hour),
		},
	}
	svc := NewSubscriptionService(groupRepoNoop{}, stub, nil, nil, nil)
	svc.now = func() time.Time { return now }

	_, err := svc.ScheduleQuotaReset(context.Background(), 13, true, false, false, timezone.StartOfDay(now).AddDate(0, 0, 1).Add(time.Hour))

	require.ErrorIs(t, err, ErrDailyResetTimeNotMidnight)
	require.False(t, stub.updated)
}

func TestScheduleQuotaReset_RejectsNonFutureOrPostExpiryTime(t *testing.T) {
	now := time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC)
	stub := &scheduleQuotaResetRepoStub{
		sub: &UserSubscription{
			ID:        12,
			UserID:    22,
			GroupID:   33,
			Status:    SubscriptionStatusActive,
			StartsAt:  now.Add(-24 * time.Hour),
			ExpiresAt: now.Add(48 * time.Hour),
		},
	}
	svc := NewSubscriptionService(groupRepoNoop{}, stub, nil, nil, nil)
	svc.now = func() time.Time { return now }

	_, err := svc.ScheduleQuotaReset(context.Background(), 12, true, false, false, now)
	require.Error(t, err)
	require.False(t, stub.updated)

	_, err = svc.ScheduleQuotaReset(context.Background(), 12, true, false, false, stub.sub.ExpiresAt)
	require.Error(t, err)
	require.False(t, stub.updated)
}
