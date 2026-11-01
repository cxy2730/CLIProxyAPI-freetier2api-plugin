package admin

// 本文件锁住 #71 第二轮复审的两处页面回归。
//
// P1-1：has_total 为真但 total=0（CodeArts 未开通福利、Qoder 的空桶）时，
//       旧代码直接相除，页头显示 "NaN%"。
// P1-2：待复核标记原来用「last_date 为空」作前提，而 last_date 只写不清，
//       真实账号恒非空，标记一次都不会出现。

import (
	"strings"
	"testing"
)

func TestConsolePageGuardsZeroTotalDivisor(t *testing.T) {
	page := Page("/plugins/freetier2api")

	if !strings.Contains(page, "var ratio = (hasTotal && quota.total > 0) ? (quota.remain / quota.total) : 0;") {
		t.Fatal("使用率必须同时校验 has_total 与 total > 0，否则 0/0 会渲染成 NaN%")
	}
	// 旧的单条件写法必须不再存在，否则回归会悄悄回来。
	if strings.Contains(page, "var ratio = hasTotal ? (quota.remain / quota.total) : 0;") {
		t.Fatal("未加分母守卫的 ratio 写法必须删除")
	}
}

func TestConsolePageUsesBackendCheckinPending(t *testing.T) {
	page := Page("/plugins/freetier2api")

	if !strings.Contains(page, "var pendingToday = !!account.checkin_pending;") {
		t.Fatal("待复核必须读后端下发的 checkin_pending")
	}
	// 旧判据以「last_date 为空」为前提，签过到的账号永远进不去这个分支。
	if strings.Contains(page, "!checkin.last_date && !!checkin.provisional_date") {
		t.Fatal("前端不得再用 last_date 是否为空推断今天待复核")
	}
	if !strings.Contains(page, `"待复核"`) {
		t.Fatal("待复核标记必须仍然渲染出来")
	}
}
