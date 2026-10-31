package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/state"
)

// 签到链路的两条护栏：每日活跃信标本地区重、账号锁。

// fakeBeaconVendor 是「签到 = 上报每日活跃信标」的供应商替身。
type fakeBeaconVendor struct {
	*fakeCheckinVendor
	beacon bool
	calls  int
}

func (v *fakeBeaconVendor) CheckinUsesDailyBeacon() bool { return v.beacon }

func (v *fakeBeaconVendor) Checkin(ctx context.Context, credential *core.Credential) (*core.CheckinResult, error) {
	v.calls++
	return v.fakeCheckinVendor.Checkin(ctx, credential)
}

func newBeaconService(now time.Time) (*Service, *fakeDeps, *core.Credential) {
	deps := newFakeDeps(now)
	service := &Service{deps: deps}
	return service, deps, &core.Credential{FileID: "wb-user", VendorID: "workbuddyglobal"}
}

// 活跃信标按天幂等，但上游热力图是异步聚合的：同一天的第二个排程点读到
// 的可能还没亮，再上报一次就把当天计数翻倍。本地记过就不再打上游。
func TestCheckinSkipsSecondDailyBeacon(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, checkinZone)
	service, deps, credential := newBeaconService(now)
	vendor := &fakeBeaconVendor{
		fakeCheckinVendor: &fakeCheckinVendor{id: "workbuddyglobal", result: &core.CheckinResult{Credit: 2, Streak: 3}},
		beacon:            true,
	}

	first, err := service.Checkin(context.Background(), vendor, credential, CheckinTriggerSchedule)
	if err != nil {
		t.Fatalf("first checkin: %v", err)
	}
	if first.Credit != 2 || vendor.calls != 1 {
		t.Fatalf("首次签到应真正打上游: calls=%d result=%+v", vendor.calls, first)
	}

	second, err := service.Checkin(context.Background(), vendor, credential, CheckinTriggerSchedule)
	if err != nil {
		t.Fatalf("second checkin: %v", err)
	}
	if vendor.calls != 1 {
		t.Fatalf("同一天第二次签到不应再打上游，实际调用 %d 次", vendor.calls)
	}
	if !second.Already {
		t.Fatalf("第二次应返回已上报: %+v", second)
	}
	if second.Streak != 3 {
		t.Fatalf("去重命中时应沿用上次连签天数，实际 %d", second.Streak)
	}

	key := state.CheckinHistoryKey("workbuddyglobal", "wb-user")
	if got := deps.state.DailyBonusSent[key]; got != "2026-10-31" {
		t.Fatalf("本地去重凭据 = %q，期望 2026-10-31", got)
	}
}

// 非信标供应商（国内版签到）不参与去重：它有自己的幂等端点，重复调用无害。
func TestCheckinKeepsCallingForNonBeaconVendor(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, checkinZone)
	service, deps, credential := newBeaconService(now)
	vendor := &fakeBeaconVendor{
		fakeCheckinVendor: &fakeCheckinVendor{id: "workbuddycn", result: &core.CheckinResult{Credit: 1}},
		beacon:            false,
	}

	for i := 0; i < 2; i++ {
		if _, err := service.Checkin(context.Background(), vendor, credential, CheckinTriggerManual); err != nil {
			t.Fatalf("checkin %d: %v", i, err)
		}
	}
	if vendor.calls != 2 {
		t.Fatalf("非信标签到应每次都打上游，实际 %d 次", vendor.calls)
	}
	if len(deps.state.DailyBonusSent) != 0 {
		t.Fatalf("非信标签到不应写去重凭据: %+v", deps.state.DailyBonusSent)
	}
}

// 签到与任务族共用账号锁：账号正被任务占用时签到必须让路，而不是并发打上游。
func TestCheckinRefusesWhileAccountBusy(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, checkinZone)
	service, _, credential := newBeaconService(now)
	vendor := &fakeBeaconVendor{
		fakeCheckinVendor: &fakeCheckinVendor{id: "workbuddyglobal", result: &core.CheckinResult{Credit: 2}},
		beacon:            true,
	}

	if !service.tryLockAccount(credential.FileIDValue()) {
		t.Fatal("首次加锁应成功")
	}
	defer service.unlockAccount(credential.FileIDValue())

	_, err := service.Checkin(context.Background(), vendor, credential, CheckinTriggerManual)
	if err == nil || !strings.Contains(err.Error(), "正在执行其它任务") {
		t.Fatalf("账号占用时应拒绝签到，实际 err = %v", err)
	}
	if vendor.calls != 0 {
		t.Fatalf("被锁挡住时不应打上游，实际调用 %d 次", vendor.calls)
	}
}

// 签到失败要把错误交给调用方（而不是吞掉后返回空结果）。
func TestCheckinPropagatesProviderError(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, checkinZone)
	service, _, credential := newBeaconService(now)
	vendor := &fakeBeaconVendor{
		fakeCheckinVendor: &fakeCheckinVendor{id: "workbuddyglobal", err: errors.New("upstream 500")},
		beacon:            true,
	}
	if _, err := service.Checkin(context.Background(), vendor, credential, CheckinTriggerManual); err == nil {
		t.Fatal("供应商报错时应返回错误")
	}
	// 失败后账号锁必须释放，否则该账号后续所有任务都会被挡住。
	if !service.tryLockAccount(credential.FileIDValue()) {
		t.Fatal("签到失败后账号锁未释放")
	}
	service.unlockAccount(credential.FileIDValue())
}

// pickAccount：身份字段优先，展示名（Label）只在全局唯一命中时才兜底。
func TestPickAccountIgnoresAmbiguousLabel(t *testing.T) {
	entry := func(id, label string) *accountContext {
		return &accountContext{entry: core.HostAuth{ID: id, Name: id, Label: label, AuthIndex: id}}
	}
	a := entry("auth-a", "同一个昵称")
	b := entry("auth-b", "同一个昵称")
	c := entry("auth-c", "c@example.com")

	if got := pickAccount([]*accountContext{a, b, c}, "同一个昵称"); got != nil {
		t.Fatalf("Label 命中多个账号时必须判为找不到，实际 = %+v", got.entry.ID)
	}
	if got := pickAccount([]*accountContext{a, b, c}, "c@example.com"); got != c {
		t.Fatalf("Label 唯一命中应返回该账号，实际 = %+v", got)
	}
	if got := pickAccount([]*accountContext{a, b, c}, "auth-b"); got != b {
		t.Fatalf("身份字段命中应返回该账号，实际 = %+v", got)
	}
	if got := pickAccount(nil, "auth-a"); got != nil {
		t.Fatal("空候选应返回 nil")
	}
}
