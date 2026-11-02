package admin

import (
	"net/http"
	"strings"

	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/state"
	"freetier2api-plugin/internal/tasks"
)

type settingsUpdateRequest struct {
	AutoCheckin   *bool            `json:"auto_checkin"`
	AutoCheckinAt string           `json:"auto_checkin_at"`
	AutoTasks     *bool            `json:"auto_tasks"`
	TaskSchedules map[string][]int `json:"task_schedules"`

	// Deprecated: 兼容旧版页面/脚本的固定字段。
	CheckinHours   []int `json:"checkin_hours"`
	TravelHours    []int `json:"travel_hours"`
	ActivityHours  []int `json:"activity_hours"`
	KeepaliveHours []int `json:"keepalive_hours"`
	BlackcatHours  []int `json:"blackcat_hours"`
}

func (s *Service) handleSettingsRequest(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	body, errDecode := decodeRequest[settingsUpdateRequest](req.Body)
	if errDecode != nil {
		return BadJSONRequestResponse()
	}
	// 固定签到时刻在校验阶段就挡掉：它以前在 Mutate 内部静默忽略非法值，
	// 用户看到 200 以为存进去了，实际没生效。
	autoCheckinAt := strings.TrimSpace(body.AutoCheckinAt)
	if autoCheckinAt != "" {
		if _, _, errClock := ParseClock(autoCheckinAt); errClock != nil {
			return ErrorResponse(core.NewPluginError("auto_checkin_at_invalid",
				"固定签到时刻格式应为 HH:MM（24 小时制）", http.StatusBadRequest))
		}
	}
	changed := map[string]any{}
	s.deps.MutateState(func(current *state.State) {
		if body.AutoCheckin != nil {
			current.AutoCheckin = body.AutoCheckin
			changed["auto_checkin"] = *body.AutoCheckin
		}
		if autoCheckinAt != "" {
			current.AutoCheckinAt = autoCheckinAt
			changed["auto_checkin_at"] = autoCheckinAt
		}
		if body.AutoTasks != nil {
			current.AutoTasks = body.AutoTasks
			changed["auto_tasks"] = *body.AutoTasks
		}
		if current.TaskSchedules == nil {
			current.TaskSchedules = map[string][]int{}
		}
		for kind, hours := range body.TaskSchedules {
			kind = strings.TrimSpace(kind)
			if kind == "" {
				continue
			}
			if _, known := s.tasks.Catalog().SpecByKind(kind); !known {
				continue
			}
			current.TaskSchedules[kind] = tasks.NormalizeHours(hours)
			changed["task_schedules"] = current.TaskSchedules
		}
		for kind, hours := range map[string][]int{
			"checkin":   body.CheckinHours,
			"travel":    body.TravelHours,
			"activity":  body.ActivityHours,
			"keepalive": body.KeepaliveHours,
			"blackcat":  body.BlackcatHours,
		} {
			if hours != nil {
				current.TaskSchedules[kind] = tasks.NormalizeHours(hours)
				changed["task_schedules"] = current.TaskSchedules
			}
		}
	})
	if errReconfigure := s.deps.Reconfigure(); errReconfigure != nil {
		return ErrorResponse(errReconfigure)
	}
	s.tasks.Poke()
	return JSONResponse(http.StatusOK, map[string]any{
		"ok": true, "changed": changed, "settings": s.settingsView(),
	})
}

// settingsView 是 /status 的 settings 载荷：任务引擎的可读设置。
func (s *Service) settingsView() map[string]any {
	return s.tasks.EffectiveSettings()
}
