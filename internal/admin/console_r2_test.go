package admin

// 本文件锁住 #71 第二轮复审的两处页面回归。
//
// P1-1：has_total 为真但 total=0（CodeArts 未开通福利、Qoder 的空桶）时，
//       旧代码直接相除，页头显示 "NaN%"；也不能按 0% 画红槽，那读起来像
//       「额度已经用光」，而事实是「没有可用的总额」。
// P1-2：待复核标记原来用「last_date 为空」作前提，而 last_date 只写不清，
//       真实账号恒非空，标记一次都不会出现。

import (
	"strings"
	"testing"
)

func TestConsolePageGuardsZeroTotalDivisor(t *testing.T) {
	page := Page("/plugins/freetier2api")

	// 分母守卫必须只算一次，进度条与百分比共用同一判据。
	if !strings.Contains(page, "var ratioKnown = hasTotal && quota.total > 0;") {
		t.Fatal("剩余率必须同时校验 has_total 与 total > 0，否则 0/0 会渲染成 NaN%")
	}
	if !strings.Contains(page, "var ratio = ratioKnown ? (quota.remain / quota.total) : 0;") {
		t.Fatal("ratio 必须由 ratioKnown 派生，不能各自重复判断")
	}
	// 进度条与百分比都必须走 ratioKnown；只守 ratio 而放任 hasTotal 画条，
	// 0/0 仍会渲染成一条 0% 的红槽（= 「额度用光」的错误暗示）。
	if strings.Contains(page, "if (hasTotal) {\n          // 阈值着色") {
		t.Fatal("进度条分支必须用 ratioKnown，不能只看 hasTotal")
	}
	if !strings.Contains(page, `ratioKnown ? Math.round(ratio * 100) + "%" : "—"`) {
		t.Fatal("百分比在分母不可用时必须渲染成 —")
	}
	// 旧的单条件写法必须不再存在，否则回归会悄悄回来。
	if strings.Contains(page, "var ratio = hasTotal ? (quota.remain / quota.total) : 0;") {
		t.Fatal("未加分母守卫的 ratio 写法必须删除")
	}
	// 总额为 0 时要给出悬浮说明，别让人以为额度真的用光了。
	if !strings.Contains(page, `remainCell.title = "上游报了总额但它是 0，算不出剩余比例"`) {
		t.Fatal("total=0 时剩余列需要悬浮说明")
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
	// checkin_today 必须被用上：只下不发就是死字段。
	if !strings.Contains(page, "var confirmedToday = !!account.checkin_today;") {
		t.Fatal("checkin_today 下发后必须被页面消费")
	}
}
