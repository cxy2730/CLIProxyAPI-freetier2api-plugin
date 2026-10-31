package tasks

// 本文件是签到执行的唯一入口：调用供应商能力，并把结果落成
// 「每账号最近一次」（state.Checkin）与「滚动历史」（state.CheckinHistory）。
//
// 定时调度、控制台一键签到、宿主额度重置三条路径都走这里。此前三条路径各自
// 复制了「断言能力 → 调用 → 判空 → 落库」，且失败路径完全不落库，于是
// 「横幅显示 N 个失败」之后无处可查：失败账号、报错原文、哪次调度触发的，
// 全都只活在几秒钟的横幅里。

import (
	"context"
	"fmt"
	"time"

	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/state"
)

// CheckinTrigger 标识一次签到是谁触发的（写进历史的 trigger 字段）。
type CheckinTrigger string

const (
	// CheckinTriggerSchedule 是定时调度触发。
	CheckinTriggerSchedule CheckinTrigger = "schedule"
	// CheckinTriggerManual 是控制台一键签到或宿主额度重置触发。
	CheckinTriggerManual CheckinTrigger = "manual"
)

// 签到历史里的结果状态（success / already / error）。
const (
	checkinStatusSuccess = "success"
	checkinStatusAlready = "already"
	checkinStatusError   = "error"
)

// Checkin 执行一次签到，并记录最近一次结果与滚动历史。
//
// 返回值原样透传供应商结果与错误，调用方按自己的语义决定如何展示；
// 落库只在这里发生一次，避免三条路径各写一份。
func (s *Service) Checkin(ctx context.Context, vendor core.Vendor, credential *core.Credential, trigger CheckinTrigger) (*core.CheckinResult, error) {
	provider, okProvider := core.As[core.CheckinProvider](vendor)
	if !okProvider {
		return nil, fmt.Errorf("本供应商不提供签到")
	}
	if credential == nil {
		return nil, fmt.Errorf("账号不存在")
	}
	// 签到与任务族共用同一把账号锁（键是 FileID，见 accountContext.uid）：
	// 签到请求打上游的同时，开学季 / 连登也在同一账号上跑，上游按
	// 「同账号并发会话」判风控，会随机回 429 或直接判失败。锁不上就让
	// 调用方稍后再试，不硬闯。
	uid := credential.FileIDValue()
	if uid == "" {
		// 没有账号身份就无法串行化，也写不进状态；直接说清楚，而不是给出
		// 「该账号正在执行其它任务」这种误导性的锁冲突文案。
		return nil, fmt.Errorf("账号缺少文件标识，无法执行签到")
	}
	if !s.tryLockAccount(uid) {
		return nil, fmt.Errorf("该账号正在执行其它任务，请稍后再试")
	}
	defer s.unlockAccount(uid)

	// 每日活跃信标本地区重：上游热力图是异步聚合的，同一天的第二个排程点
	// 读到的可能还是没亮，再上报一次就会把当天计数翻倍。
	//
	// 去重凭据与上次连签天数一次快照读出来：State() 是整份深拷贝，逐字段读
	// 等于为每个字段复制一遍状态。
	if beacon := dailyBeacon(vendor); beacon {
		key := checkinHistoryKey(vendor, credential)
		snapshot := s.deps.State()
		if key != "" && snapshot.DailyBonusSent[key] == s.todayString() {
			result := &core.CheckinResult{
				Already: true,
				Message: "今日活跃信标已上报过（本地记录），跳过重复上报",
				Streak:  snapshot.Checkin[uid].Streak,
			}
			s.recordCheckinResult(vendor, credential, trigger, result)
			return result, nil
		}
	}

	result, errCheckin := provider.Checkin(ctx, credential)
	if errCheckin != nil {
		s.recordCheckinFailure(vendor, credential, trigger, errCheckin.Error())
		return nil, errCheckin
	}
	if result == nil {
		s.recordCheckinFailure(vendor, credential, trigger, "签到未返回结果")
		return nil, fmt.Errorf("签到未返回结果")
	}
	s.recordCheckinResult(vendor, credential, trigger, result)
	return result, nil
}

// dailyBeacon 报告该供应商当前是否走每日活跃信标签到。
func dailyBeacon(vendor core.Vendor) bool {
	beacon, okBeacon := core.As[core.DailyBeaconVendor](vendor)
	return okBeacon && beacon.CheckinUsesDailyBeacon()
}

// checkinResultStatus 把供应商结果映射成历史状态。
//
// NotRecorded（窗口未开启 / 领取被拒）记成 error：它和失败一样「本次没领到」，
// 记成 success 会让历史看起来签到成功，而积分并没到账。
func checkinResultStatus(result *core.CheckinResult) string {
	switch {
	case result.NotRecorded:
		return checkinStatusError
	case result.Already:
		return checkinStatusAlready
	default:
		return checkinStatusSuccess
	}
}

// recordCheckinResult 写入「最近一次」与滚动历史。
//
// 两件事在同一次 Mutate 里落盘：一次签到只产生一次状态变更，写两次会让
// 含模型缓存的 state.json 被整份重写两遍。
func (s *Service) recordCheckinResult(vendor core.Vendor, credential *core.Credential, trigger CheckinTrigger, result *core.CheckinResult) {
	key := checkinHistoryKey(vendor, credential)
	if key == "" || result == nil {
		return
	}
	uid := credential.FileIDValue()
	entry := s.checkinEntry(trigger, checkinResultStatus(result), result.Message, result.Credit)
	today := s.todayString()
	limit := s.checkinHistoryLimit()
	// 活跃信标按天幂等：本地记一笔「今天报过了」，供后续排程点去重。
	beacon := dailyBeacon(vendor)
	s.deps.MutateState(func(current *state.State) {
		// 供应商显式声明「本次未完成今日签到」时不更新「最近一次」：把
		// 「上游窗口未开启」之类的状态记成今日已签到，会让页面显示假性成功，
		// 而上游窗口错过不补领——用户以为领到了，实际积分永远拿不到。
		if !result.NotRecorded {
			record := current.Checkin[uid]
			record.LastDate = today
			if result.Credit > 0 {
				record.Credit = result.Credit
			}
			if result.Energy > 0 {
				record.Energy = result.Energy
			}
			// 连签天数由供应商透传；上游没给（0）时保留上一次的值，
			// 不能用「今天没返回」把已有天数清成 0。
			if result.Streak > 0 {
				record.Streak = result.Streak
			}
			current.Checkin[uid] = record
			// 活跃信标按天幂等，本地记一笔「今天报过了」，供后续排程点去重。
			if beacon {
				if current.DailyBonusSent == nil {
					current.DailyBonusSent = map[string]string{}
				}
				current.DailyBonusSent[key] = today
			}
		}
		pushCheckinHistory(current, key, entry, limit)
	})
}

// recordCheckinFailure 记录一次拿不到结果的失败（供应商报错 / 未返回结果）。
//
// 失败路径没有「最近一次」可更新，只写历史——这正是过去缺失的部分。
func (s *Service) recordCheckinFailure(vendor core.Vendor, credential *core.Credential, trigger CheckinTrigger, message string) {
	key := checkinHistoryKey(vendor, credential)
	if key == "" {
		return
	}
	entry := s.checkinEntry(trigger, checkinStatusError, message, 0)
	limit := s.checkinHistoryLimit()
	s.deps.MutateState(func(current *state.State) {
		pushCheckinHistory(current, key, entry, limit)
	})
}

// checkinEntry 构造一条历史记录（时间取注入的时钟）。
func (s *Service) checkinEntry(trigger CheckinTrigger, status, message string, credit int64) state.CheckinHistoryEntry {
	return state.CheckinHistoryEntry{
		RunAt:   s.deps.Now().Format(time.RFC3339),
		Trigger: string(trigger),
		Status:  status,
		Message: message,
		Credit:  credit,
	}
}

// pushCheckinHistory 把记录插到最前（新→旧）并按上限截断；调用方持有 Mutate 锁。
func pushCheckinHistory(current *state.State, key string, entry state.CheckinHistoryEntry, limit int) {
	history := make([]state.CheckinHistoryEntry, 0, len(current.CheckinHistory[key])+1)
	history = append(history, entry)
	history = append(history, current.CheckinHistory[key]...)
	if len(history) > limit {
		history = history[:limit]
	}
	current.CheckinHistory[key] = history
}

// checkinHistoryKey 返回该账号的历史键；身份不完整时为空串（不落库）。
func checkinHistoryKey(vendor core.Vendor, credential *core.Credential) string {
	if vendor == nil || credential == nil {
		return ""
	}
	return state.CheckinHistoryKey(vendor.ID(), credential.FileIDValue())
}

// checkinHistoryLimit 返回生效的历史条数上限（未配置时用默认值）。
func (s *Service) checkinHistoryLimit() int {
	if limit := s.deps.Config().CheckinHistoryLimit; limit > 0 {
		return limit
	}
	return state.DefaultCheckinHistoryLimit
}

// checkinZone 是上游每日签到重置使用的时区（固定 UTC+8）。
//
// 不能用宿主机本地时区：上游按北京时间零点重置，海外主机（UTC-7 等）在
// 本地 10:00 触发时北京时间还是前一天，会把「上一轮已领」记成今天已签到，
// 导致当天真正刷新后不再重试。
var checkinZone = time.FixedZone("UTC+8", 8*60*60)

// todayString 返回签到日键（UTC+8）。
func (s *Service) todayString() string { return s.deps.Now().In(checkinZone).Format("2006-01-02") }
