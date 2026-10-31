package workbuddy

import (
	"testing"
	"time"
)

// 连续点亮天数的口径测试。
//
// 热力图给的是「点亮过的日期集合」，不是连签计数。此前直接用 len(lit) 当
// 「已连续点亮 N 天」，断签之后那个数字只增不减，页面上永远是假的。

func TestConsecutiveLitDays(t *testing.T) {
	today := "2026-10-31"
	cases := []struct {
		name string
		lit  []string
		want int
	}{
		{"空集合", nil, 0},
		{"只有今天", []string{"2026-10-31"}, 1},
		{"今天往前三天", []string{"2026-10-29", "2026-10-30", "2026-10-31"}, 3},
		{"今天还没点亮但昨天起连续两天", []string{"2026-10-29", "2026-10-30"}, 2},
		{"中间断一天只算到今天前", []string{"2026-10-28", "2026-10-30", "2026-10-31"}, 2},
		{"断了很久的历史不参与", []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-10-31"}, 1},
		{"乱序不影响", []string{"2026-10-31", "2026-10-29", "2026-10-30"}, 3},
		{"重复日期只算一天", []string{"2026-10-30", "2026-10-30", "2026-10-31"}, 2},
		{"带空白的日期", []string{" 2026-10-30 ", "2026-10-31"}, 2},
		{"跨月连续", []string{"2026-09-30", "2026-10-01"}, 0},
	}
	for _, item := range cases {
		if got := consecutiveLitDays(item.lit, today); got != item.want {
			t.Errorf("%s: consecutiveLitDays(%v) = %d, 期望 %d", item.name, item.lit, got, item.want)
		}
	}
}

// TestConsecutiveLitDaysCrossMonth 验证跨月不会算断：9-30 → 10-01 → 10-02 连续三天。
func TestConsecutiveLitDaysCrossMonth(t *testing.T) {
	lit := []string{"2026-09-30", "2026-10-01", "2026-10-02"}
	if got := consecutiveLitDays(lit, "2026-10-02"); got != 3 {
		t.Errorf("跨月连续 = %d, 期望 3", got)
	}
}

// TestConsecutiveLitDaysBadToday 验证非法日期不会 panic、返回 0。
func TestConsecutiveLitDaysBadToday(t *testing.T) {
	if got := consecutiveLitDays([]string{"2026-10-31"}, "not-a-date"); got != 0 {
		t.Errorf("非法日期 = %d, 期望 0", got)
	}
}

// TestConsecutiveLitDaysTodayIsCST 验证 today 按东八区解析：
// 若按 UTC 解析，「10-31 07:00 CST」这种时刻会被算成 10-30。
func TestConsecutiveLitDaysTodayIsCST(t *testing.T) {
	if cstZone == nil {
		t.Fatal("cstZone 未定义")
	}
	now := time.Date(2026, 10, 31, 7, 0, 0, 0, cstZone)
	if got := consecutiveLitDays([]string{"2026-10-31"}, now.Format("2006-01-02")); got != 1 {
		t.Errorf("东八区当天 = %d, 期望 1", got)
	}
}
