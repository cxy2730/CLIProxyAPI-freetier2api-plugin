// Package state 负责插件自己的落盘状态：state.json 的类型化读写。
//
// 状态里不保存任何凭证：凭证只存在于宿主的 auth 文件中，所有需要 token
// 的操作都必须通过 host.auth.get 实时回源。
//
// 本包只管「读、改、写、快照」四件事；机器盐、日志目录等有副作用的初始化
// 留在根层（ABI 适配层）。
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/logger"
)

// Version 是状态文件结构版本，用于将来迁移。
const Version = 1

// TaskRecord 是某账号某任务的最近一次执行记录（仅用于展示，不作幂等依据）。
type TaskRecord struct {
	LastRunAt   string `json:"last_run_at,omitempty"`
	LastStatus  string `json:"last_status,omitempty"`
	LastMessage string `json:"last_message,omitempty"`
	Credit      int64  `json:"credit,omitempty"`
	Energy      int64  `json:"energy,omitempty"`
}

// CheckinRecord 是某账号的签到记录。
type CheckinRecord struct {
	LastDate string `json:"last_date,omitempty"`
	Streak   int64  `json:"streak,omitempty"`
	Credit   int64  `json:"credit,omitempty"`
	Energy   int64  `json:"energy,omitempty"`
}

// DefaultCheckinHistoryLimit 是每个账号默认保留的签到历史条数。
const DefaultCheckinHistoryLimit = 30

// CheckinHistoryEntry 是一次签到执行的记录（滚动历史，每个账号只留最近若干条）。
//
// 不含 account / vendor 字段：身份由 map 键承载（见 CheckinHistoryKey）。
// 账号 label 在 apikey 型供应商里就是密钥本身，抄进 state.json 等于把凭证落盘。
type CheckinHistoryEntry struct {
	// RunAt 是本次执行时间（RFC3339）。
	RunAt string `json:"run_at"`
	// Trigger 是触发方式：schedule（定时调度）/ manual（手动或额度重置）。
	Trigger string `json:"trigger,omitempty"`
	// Status 是本次结果：success / already / error。
	Status string `json:"status"`
	// Message 是本次结果的说明；失败时即报错原文。
	Message string `json:"message,omitempty"`
	// Credit 是本次所得积分。
	Credit int64 `json:"credit,omitempty"`
}

// CheckinHistoryKey 返回签到历史的账号键：供应商 + 账号身份（FileID 优先）。
//
// 键型与额度缓存一致（vendor + 账号身份）：Name / Label 跨供应商并不唯一，
// 少了供应商维度就会让不同渠道的同名账号互相串记录。任一为空时返回空键，
// 调用方据此放弃落库。
func CheckinHistoryKey(vendorID, accountID string) string {
	vendorID = strings.TrimSpace(vendorID)
	accountID = strings.TrimSpace(accountID)
	if vendorID == "" || accountID == "" {
		return ""
	}
	return vendorID + "\x00" + accountID
}

// ClaimRecord 是某账号最近一次的套餐领取状态。
//
// 内嵌 pluginapi.ClaimStatus 而不是复述它的字段：额度响应与页面展示共用
// 同一份形状，供应商改字段时这里不用跟着改。
type ClaimRecord struct {
	pluginapi.ClaimStatus
	// CheckedAt 是本次探测时间（RFC3339），页面据此显示「多久前查的」。
	CheckedAt string `json:"checked_at,omitempty"`
}

// State 是 state.json 的完整结构。
//
// 页面设置用指针字段区分「未设置」与「设为 false/空」：
// 未设置时回落 YAML 配置，设置了就以页面为准。
type State struct {
	Version int `json:"version"`

	// AutoCheckin / AutoCheckinAt / AutoTasks 是页面上可改的任务设置。
	AutoCheckin   *bool  `json:"auto_checkin,omitempty"`
	AutoCheckinAt string `json:"auto_checkin_at,omitempty"`
	AutoTasks     *bool  `json:"auto_tasks,omitempty"`
	// PromptMode 是页面上改过的提示词过滤模式；空表示沿用 YAML 配置。
	PromptMode string `json:"prompt_mode,omitempty"`
	// ModelNameTag 是页面上改过的模型名渠道标识模式（off/prefix/suffix）；
	// 空表示沿用 YAML 配置。
	ModelNameTag string `json:"model_name_tag,omitempty"`

	// TaskSchedules 是按任务族保存的整点小时列表（0-23）。
	// 新代码只读写这个字段；下面五个字段仅用于读取旧状态并迁移。
	TaskSchedules map[string][]int `json:"task_schedules,omitempty"`

	// Deprecated: 旧版固定字段，仅用于迁移读取。
	CheckinHours   []int `json:"checkin_hours,omitempty"`
	TravelHours    []int `json:"travel_hours,omitempty"`
	ActivityHours  []int `json:"activity_hours,omitempty"`
	KeepaliveHours []int `json:"keepalive_hours,omitempty"`
	BlackcatHours  []int `json:"blackcat_hours,omitempty"`

	// Models 是上游模型清单缓存（按 realm 分桶存于 Realm 字段）。
	Models []core.CachedModel `json:"models,omitempty"`
	// ModelsFetchedAt 是清单缓存时间（RFC3339）。
	ModelsFetchedAt string `json:"models_fetched_at,omitempty"`

	// DisabledModels 是被禁用的模型 ID（带 cn:/global: 前缀的完整 ID）。
	DisabledModels []string `json:"disabled_models,omitempty"`

	// ModelAliases 是模型别名表：<vendorID>:<官方模型 ID> → 对外别名。
	ModelAliases map[string]string `json:"model_aliases,omitempty"`

	// AccountAliases 是账号别名表：账号身份（FileID）→ 展示别名。
	//
	// 键必须是稳定账号身份，不能用昵称/邮箱：前者跨账号不唯一、上游改一次就变，
	// 拿它做键会让别名在重登后丢失，或串到另一个账号头上。FileID 只由凭证
	// 文件名决定，token 刷新与重登都不动它（见 core.Credential.FileID）。
	AccountAliases map[string]string `json:"account_aliases,omitempty"`

	// RestartPending 标记「已有模型变更，但宿主的模型注册表尚未刷新」。
	RestartPending bool `json:"restart_pending,omitempty"`

	// Checkin 按账号 UID 记录签到状态。
	Checkin map[string]CheckinRecord `json:"checkin,omitempty"`
	// CheckinHistory 按 CheckinHistoryKey 保存每个账号最近的签到执行记录（新→旧）。
	CheckinHistory map[string][]CheckinHistoryEntry `json:"checkin_history,omitempty"`
	// DailyBonusSent 按 CheckinHistoryKey 记录最近一次「已上报每日活跃信标」的日期。
	//
	// 国际版签到走的是每日活跃信标：上游按天幂等，但热力图是异步聚合的，
	// 同一天的第二个排程点读到的热力图可能还没亮，于是重复上报把当天计数
	// 翻倍（2 → 4）。上游没有可查的幂等键，只能在这里记一笔本地凭据。
	DailyBonusSent map[string]string `json:"daily_bonus_sent,omitempty"`
	// Tasks 按「uid\x1f任务码」记录任务执行历史。
	Tasks map[string]TaskRecord `json:"tasks,omitempty"`
	// Claim 按账号 FileID 记录套餐领取状态（额度查询时顺带刷新）。
	Claim map[string]ClaimRecord `json:"claim,omitempty"`
}

// Store 是 state.json 的进程内单例：串行化读改写并缓存快照。
type Store struct {
	mu    sync.Mutex
	dir   string
	cache *State
	// dirty 表示内存状态比磁盘新；flushing 表示已有一个写盘任务在排队。
	dirty    bool
	flushing bool
	// flushTimer 是挂起的合并写盘定时器。
	//
	// 它和任务/后台动作一样属于插件生命周期：只 Flush 不取消定时器，插件被
	// 卸载后它仍会醒来写盘（Stop 会取消它）。
	flushTimer *time.Timer
}

// NewStore 创建一个尚未绑定目录的 Store。
func NewStore() *Store { return &Store{} }

// Configure 绑定状态目录（不读写文件）。
func (s *Store) Configure(dir string) {
	s.mu.Lock()
	s.dir = dir
	s.mu.Unlock()
}

// New 返回一个补齐 map 的空状态。
func New() *State {
	return &State{
		Version:        Version,
		Checkin:        map[string]CheckinRecord{},
		CheckinHistory: map[string][]CheckinHistoryEntry{},
		DailyBonusSent: map[string]string{},
		Tasks:          map[string]TaskRecord{},
		Claim:          map[string]ClaimRecord{},
		ModelAliases:   map[string]string{},
		AccountAliases: map[string]string{},
		TaskSchedules:  map[string][]int{},
	}
}

// Load 从磁盘读取状态。文件缺失或损坏时返回空状态（不致命）。
//
// 返回值即缓存本身；调用方只应读用它，修改必须走 Mutate/Snapshot。
func (s *Store) Load() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.dirty = false
	path := s.pathLocked()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			s.cache = New()
			return s.cache, nil
		}
		return nil, fmt.Errorf("read state %s: %w", path, errRead)
	}
	var loaded State
	if errUnmarshal := json.Unmarshal(raw, &loaded); errUnmarshal != nil {
		// 状态损坏不阻止插件启动：宁可丢掉签到记录，也不要让用户无法使用。
		// 但损坏的文件不能就地覆盖——它是唯一可能被手工救回的东西，
		// 改名留存，下一次写入才不会把它抹掉。
		quarantined := quarantine(path)
		logger.Error("state file %s is corrupt, quarantined as %s, starting with empty state: %v", path, quarantined, errUnmarshal)
		s.cache = New()
		return s.cache, nil
	}
	normalize(&loaded)
	s.cache = &loaded
	return s.cache, nil
}

func (s *Store) pathLocked() string {
	return filepath.Join(s.dir, "state.json")
}

func normalize(state *State) {
	state.Version = Version
	if state.TaskSchedules == nil {
		state.TaskSchedules = map[string][]int{}
	}
	for kind, hours := range map[string][]int{
		"checkin":   state.CheckinHours,
		"travel":    state.TravelHours,
		"activity":  state.ActivityHours,
		"keepalive": state.KeepaliveHours,
		"blackcat":  state.BlackcatHours,
	} {
		if len(hours) > 0 {
			if _, exists := state.TaskSchedules[kind]; !exists {
				state.TaskSchedules[kind] = append([]int(nil), hours...)
			}
		}
	}
	// 迁移后清空旧字段，后续保存不会再写它们。
	state.CheckinHours = nil
	state.TravelHours = nil
	state.ActivityHours = nil
	state.KeepaliveHours = nil
	state.BlackcatHours = nil
	if state.Checkin == nil {
		state.Checkin = map[string]CheckinRecord{}
	}
	if state.CheckinHistory == nil {
		state.CheckinHistory = map[string][]CheckinHistoryEntry{}
	}
	if state.DailyBonusSent == nil {
		state.DailyBonusSent = map[string]string{}
	}
	if state.Tasks == nil {
		state.Tasks = map[string]TaskRecord{}
	}
	if state.Claim == nil {
		state.Claim = map[string]ClaimRecord{}
	}
	if state.ModelAliases == nil {
		state.ModelAliases = map[string]string{}
	}
	if state.AccountAliases == nil {
		state.AccountAliases = map[string]string{}
	}
}

// Snapshot 返回状态深拷贝，调用方可安全读取。
func (s *Store) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		return *New()
	}
	return clone(s.cache)
}

// clone 手工深拷贝 map/slice 与指针字段。
func clone(state *State) State {
	out := *state
	out.Models = append([]core.CachedModel(nil), state.Models...)
	out.TaskSchedules = make(map[string][]int, len(state.TaskSchedules))
	for kind, hours := range state.TaskSchedules {
		out.TaskSchedules[kind] = append([]int(nil), hours...)
	}
	out.CheckinHours = nil
	out.TravelHours = nil
	out.ActivityHours = nil
	out.KeepaliveHours = nil
	out.BlackcatHours = nil
	out.DisabledModels = append([]string(nil), state.DisabledModels...)
	if state.AutoCheckin != nil {
		enabled := *state.AutoCheckin
		out.AutoCheckin = &enabled
	}
	if state.AutoTasks != nil {
		enabled := *state.AutoTasks
		out.AutoTasks = &enabled
	}
	out.Checkin = make(map[string]CheckinRecord, len(state.Checkin))
	for key, value := range state.Checkin {
		out.Checkin[key] = value
	}
	out.CheckinHistory = make(map[string][]CheckinHistoryEntry, len(state.CheckinHistory))
	for key, history := range state.CheckinHistory {
		out.CheckinHistory[key] = append([]CheckinHistoryEntry(nil), history...)
	}
	out.DailyBonusSent = make(map[string]string, len(state.DailyBonusSent))
	for key, date := range state.DailyBonusSent {
		out.DailyBonusSent[key] = date
	}
	out.Tasks = make(map[string]TaskRecord, len(state.Tasks))
	for key, value := range state.Tasks {
		out.Tasks[key] = value
	}
	out.Claim = make(map[string]ClaimRecord, len(state.Claim))
	for key, value := range state.Claim {
		out.Claim[key] = value
	}
	out.ModelAliases = make(map[string]string, len(state.ModelAliases))
	for key, value := range state.ModelAliases {
		out.ModelAliases[key] = value
	}
	out.AccountAliases = make(map[string]string, len(state.AccountAliases))
	for key, value := range state.AccountAliases {
		out.AccountAliases[key] = value
	}
	return out
}

// saveDelay 是合并写盘的窗口。
//
// 一次任务轮询里每个账号都要落一次状态，每次都整份重写 state.json（含模型
// 缓存）会把磁盘 IO 放大到账号数倍。合并到一个短窗口里写一次即可；代价是
// 进程被强杀时最多丢掉窗口内的改动（签到记录 / 模型缓存，都可重建）。
const saveDelay = 200 * time.Millisecond

// Mutate 在锁内修改状态，并安排一次落盘。
//
// 修改函数不应做 IO（它在锁内执行）；落盘失败只记日志，不让调用方失败——
// 状态写不进去不应该让一次签到或一次对话失败。
//
// 写盘在窗口末尾由后台完成（见 saveDelay），连续多次 Mutate 只写一次。
func (s *Store) Mutate(mutate func(*State)) {
	s.mu.Lock()
	if s.cache == nil {
		s.cache = New()
	}
	mutate(s.cache)
	s.dirty = true
	if !s.flushing {
		s.flushing = true
		s.flushTimer = time.AfterFunc(saveDelay, s.flush)
	}
	s.mu.Unlock()
}

// Flush 立即把未落盘的修改写入磁盘，并取消挂起的定时器。
//
// 插件关停路径必须走它：只写盘不取消定时器，卸载后定时器仍会醒来碰状态。
func (s *Store) Flush() { s.flush() }

func (s *Store) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushing = false
	// 显式 Flush/Stop 时取消挂起的定时器，避免它在关停后再写一次。
	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}
	if !s.dirty {
		return
	}
	if errSave := s.saveLocked(); errSave != nil {
		// 保留 dirty：下一次 Mutate 或 Flush 会再试一次。
		logger.Error("save state failed: %v", errSave)
		return
	}
	s.dirty = false
}

// saveLocked 原子写入状态文件。调用方必须持有 s.mu。
func (s *Store) saveLocked() error {
	if s.cache == nil {
		return nil
	}
	s.cache.Version = Version
	raw, errMarshal := json.MarshalIndent(s.cache, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal state: %w", errMarshal)
	}
	path := s.pathLocked()
	tmp := path + ".tmp"
	file, errCreate := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if errCreate != nil {
		return fmt.Errorf("write state: %w", errCreate)
	}
	if _, errWrite := file.Write(raw); errWrite != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write state: %w", errWrite)
	}
	// 落盘前 fsync：否则掉电后可能留下一个长度对、内容全零的 state.json，
	// 下次启动只能按损坏处理，签到历史整份丢失。
	if errSync := file.Sync(); errSync != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync state: %w", errSync)
	}
	if errClose := file.Close(); errClose != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close state: %w", errClose)
	}
	backup(path)
	if errRename := os.Rename(tmp, path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace state file: %w", errRename)
	}
	return nil
}

// backup 把当前状态文件留一份 .bak，供 JSON 被写坏时手工救回。
//
// 优先硬链接（零拷贝，rename 之后旧 inode 仍然完整）；不支持硬链接的
// 文件系统退回复制。备份失败不影响主流程。
func backup(path string) {
	bak := path + ".bak"
	if _, errStat := os.Stat(path); errStat != nil {
		return
	}
	_ = os.Remove(bak)
	if errLink := os.Link(path, bak); errLink == nil {
		return
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return
	}
	if errWrite := os.WriteFile(bak, raw, 0o600); errWrite != nil {
		logger.Error("backup state failed: %v", errWrite)
	}
}

// quarantine 把损坏的状态文件改名留存，返回新路径（失败时返回空串）。
func quarantine(path string) string {
	target := fmt.Sprintf("%s.corrupt-%s", path, time.Now().Format("20060102-150405"))
	if errRename := os.Rename(path, target); errRename != nil {
		logger.Error("quarantine corrupt state failed: %v", errRename)
		return ""
	}
	return target
}

// Reset 清空内存缓存（测试用）。
func (s *Store) Reset() {
	s.mu.Lock()
	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}
	s.cache = nil
	s.dirty = false
	s.flushing = false
	s.mu.Unlock()
}
