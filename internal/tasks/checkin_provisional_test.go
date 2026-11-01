package tasks

import (
	"strings"
	"testing"
	"time"

	"freetier2api-plugin/internal/core"
)

// 北京时间 12 点前的「已签到」不可信。
//
// 上游在每日零点刚过会对**还没领到**的账号返回「已签到」：magpie 在
// 00:00:28 实测六家渠道集体误报，同一批账号到 10:54 才真领到，29 个账号里
// 14 个中招（#3326）。若把这个回答记成 last_date，页面显示「今天已签到」，
// 用户不再追问，积分就此永久丢失。

// TestRecordCheckinProvisionalBeforeNoon 验证：北京 12 点前的「已签到」只记待复核。
func TestRecordCheckinProvisionalBeforeNoon(t *testing.T) {
	credential := &core.Credential{FileID: "qodercn-user"}
	// 北京 08:00 = UTC 00:00，正是误报高发时段。
	deps := newFakeDeps(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC))
	service := &Service{deps: deps}

	service.recordCheckinResult(&fakeCheckinVendor{id: "qodercn"}, credential, CheckinTriggerSchedule, &core.CheckinResult{
		Already: true,
		Message: "今日已签到",
	})

	record := deps.state.Checkin["qodercn-user"]
	if record.LastDate != "" {
		t.Fatalf("12 点前的「已签到」不得写 last_date，实际 = %q", record.LastDate)
	}
	if record.ProvisionalDate != "2026-10-21" {
		t.Fatalf("provisional_date = %q, 期望 2026-10-21", record.ProvisionalDate)
	}
	// 待复核不算「今天报过信标」：那一笔额度还没到账，报信标会让后续排程点
	// 误以为今天已经走完，从而再也不复核。
	if sent, ok := deps.state.DailyBonusSent["qodercn-user"]; ok {
		t.Fatalf("待复核不得写活跃信标，实际 = %q", sent)
	}
}

// TestRecordCheckinProvisionalBoundary 验证：12 点是分界，11:59 仍待复核、12:00 起可信。
func TestRecordCheckinProvisionalBoundary(t *testing.T) {
	cases := []struct {
		name        string
		utc         time.Time
		wantLast    string
		wantPending string
	}{
		{
			name:        "北京 11:59 仍待复核",
			utc:         time.Date(2026, 10, 21, 3, 59, 0, 0, time.UTC),
			wantPending: "2026-10-21",
		},
		{
			name:     "北京 12:00 起可信",
			utc:      time.Date(2026, 10, 21, 4, 0, 0, 0, time.UTC),
			wantLast: "2026-10-21",
		},
		{
			name:     "北京 23:00 可信",
			utc:      time.Date(2026, 10, 21, 15, 0, 0, 0, time.UTC),
			wantLast: "2026-10-21",
		},
	}
	for _, c := range cases {
		credential := &core.Credential{FileID: "qodercn-user"}
		deps := newFakeDeps(c.utc)
		service := &Service{deps: deps}
		service.recordCheckinResult(&fakeCheckinVendor{id: "qodercn"}, credential, CheckinTriggerSchedule, &core.CheckinResult{Already: true})

		record := deps.state.Checkin["qodercn-user"]
		if record.LastDate != c.wantLast {
			t.Fatalf("%s: last_date = %q, want %q", c.name, record.LastDate, c.wantLast)
		}
		if record.ProvisionalDate != c.wantPending {
			t.Fatalf("%s: provisional_date = %q, want %q", c.name, record.ProvisionalDate, c.wantPending)
		}
	}
}

// TestRecordCheckinProvisionalClearedByRealClaim 验证：待复核之后真领到，会清掉待复核。
func TestRecordCheckinProvisionalClearedByRealClaim(t *testing.T) {
	credential := &core.Credential{FileID: "qodercn-user"}
	deps := newFakeDeps(time.Date(2026, 10, 21, 0, 30, 0, 0, time.UTC))
	service := &Service{deps: deps}

	// 北京 08:30 误报。
	service.recordCheckinResult(&fakeCheckinVendor{id: "qodercn"}, credential, CheckinTriggerSchedule, &core.CheckinResult{Already: true})
	if deps.state.Checkin["qodercn-user"].ProvisionalDate == "" {
		t.Fatal("前置条件不成立：08:30 应记为待复核")
	}

	// 北京 16:00 真领到 100 积分。
	deps.now = time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	service.recordCheckinResult(&fakeCheckinVendor{id: "qodercn"}, credential, CheckinTriggerSchedule, &core.CheckinResult{Credit: 100})

	record := deps.state.Checkin["qodercn-user"]
	if record.LastDate != "2026-10-21" {
		t.Fatalf("真领到后 last_date = %q, 期望 2026-10-21", record.LastDate)
	}
	if record.ProvisionalDate != "" {
		t.Fatalf("真领到后应清空 provisional_date，实际 = %q", record.ProvisionalDate)
	}
	if record.Credit != 100 {
		t.Fatalf("credit = %d, 期望 100", record.Credit)
	}
}

// TestRecordCheckinRealClaimBeforeNoonStillRecords 验证：12 点前真领到照常落库。
//
// 分界只针对「已签到」这种**推断**：真拿到积分的回答没有歧义，不该被压成待复核。
func TestRecordCheckinRealClaimBeforeNoonStillRecords(t *testing.T) {
	credential := &core.Credential{FileID: "qodercn-user"}
	deps := newFakeDeps(time.Date(2026, 10, 21, 0, 10, 0, 0, time.UTC))
	service := &Service{deps: deps}

	service.recordCheckinResult(&fakeCheckinVendor{id: "qodercn"}, credential, CheckinTriggerSchedule, &core.CheckinResult{Credit: 100})

	record := deps.state.Checkin["qodercn-user"]
	if record.LastDate != "2026-10-21" {
		t.Fatalf("真领到应照常落库，last_date = %q", record.LastDate)
	}
	if record.ProvisionalDate != "" {
		t.Fatalf("真领到不该留 provisional_date，实际 = %q", record.ProvisionalDate)
	}
}

// TestCheckinResultMessageNotesProvisional 验证：待复核的说明会进历史。
func TestCheckinResultMessageNotesProvisional(t *testing.T) {
	message := checkinResultMessage(&core.CheckinResult{Already: true, Message: "今日已签到"}, true)
	if !strings.Contains(message, "今日已签到") {
		t.Fatalf("应保留上游原文，实际 = %q", message)
	}
	if !strings.Contains(message, "复核") {
		t.Fatalf("应标注待复核，实际 = %q", message)
	}
	// 非待复核时原样返回，不得篡改既有厂商的文案。
	if got := checkinResultMessage(&core.CheckinResult{Message: "今日已签到"}, false); got != "今日已签到" {
		t.Fatalf("非待复核应原样返回，实际 = %q", got)
	}
}
