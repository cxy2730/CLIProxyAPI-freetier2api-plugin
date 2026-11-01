package admin

// 本文件锁住「今天是否已签到 / 今天是否待复核」的判定。
//
// 这段逻辑原在前端，用「last_date 为空 && provisional_date 是今天」判断，
// 而 last_date 只写不清——凡是签过到的账号都恒非空，条件永假，待复核标记在
// 真实账号上一次都不会出现（#71 第二轮复审 P1-2）。现在挪到后端并加用例。

import (
	"testing"

	"freetier2api-plugin/internal/state"
)

func TestCheckinFlagsTreatsHistoricalLastDateAsPending(t *testing.T) {
	// 核心回归场景：账号昨天签过到（last_date 非空），今天上游说已签到但还没
	// 过可信时刻。旧前端逻辑在这里判定为「不待复核」。
	record := &state.CheckinRecord{}
	record.LastDate = "2026-10-30"
	record.ProvisionalDate = "2026-11-01"
	confirmed, pending := checkinFlags(record, "2026-11-01")
	if confirmed {
		t.Fatal("昨天签到今天不能算已确认")
	}
	if !pending {
		t.Fatal("provisional_date 是今天时应当判为待复核")
	}
}

func TestCheckinFlagsConfirmedTodayWinsOverPending(t *testing.T) {
	// 复核通过后 LastDate 写成今天、ProvisionalDate 被清；即使上游又写回
	// provisional，也不该在「今天已签到」的账号上挂黄色标记。
	record := &state.CheckinRecord{}
	record.LastDate = "2026-11-01"
	record.ProvisionalDate = "2026-11-01"
	confirmed, pending := checkinFlags(record, "2026-11-01")
	if !confirmed {
		t.Fatal("last_date 是今天时应当算已确认")
	}
	if pending {
		t.Fatal("已确认的账号不应再显示待复核")
	}
}

func TestCheckinFlagsIgnoresStaleProvisionalDate(t *testing.T) {
	// provisional_date 写进去不会自己清，上周那次待复核不能一直挂着。
	record := &state.CheckinRecord{}
	record.LastDate = "2026-10-28"
	record.ProvisionalDate = "2026-10-30"
	confirmed, pending := checkinFlags(record, "2026-11-01")
	if confirmed || pending {
		t.Fatalf("过期记录不应产生任何标记：confirmed=%v pending=%v", confirmed, pending)
	}
}

func TestCheckinFlagsHandlesEmptyInputs(t *testing.T) {
	if confirmed, pending := checkinFlags(nil, "2026-11-01"); confirmed || pending {
		t.Fatal("没有记录时两个标记都应为 false")
	}
	record := &state.CheckinRecord{}
	record.ProvisionalDate = "2026-11-01"
	if confirmed, pending := checkinFlags(record, ""); confirmed || pending {
		t.Fatal("取不到今天时不应给出任何结论")
	}
}
