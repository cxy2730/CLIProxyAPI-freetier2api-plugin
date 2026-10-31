package tasks

// 后台一键完成的对外契约：启动结果、并发保护、进度快照。

import (
	"net/http"
	"testing"
	"time"
)

// newAutoAllService 构造只带后台任务状态的最小 Service（不启动调度）。
func newAutoAllService() *Service {
	return &Service{
		deps:    newFakeDeps(time.Date(2026, 10, 31, 12, 0, 0, 0, checkinZone)),
		autoAll: &autoAllJob{},
	}
}

// TestStartAutoAllRejectsConcurrentRun 锁定「同一时刻只允许一个后台一键完成」。
//
// 重复点击要拿到 409 与正在执行的那个编号，前端据此接着轮询，而不是起两份
// 任务把上游打成并发。
func TestStartAutoAllRejectsConcurrentRun(t *testing.T) {
	service := newAutoAllService()
	service.autoAll.running = true
	service.autoAll.seq = 7
	service.autoAll.started = time.Date(2026, 10, 31, 12, 0, 0, 0, checkinZone)

	payload, status := service.StartAutoAll(nil)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d（已在执行时应拒绝）", status, http.StatusConflict)
	}
	if seq, _ := payload["seq"].(int64); seq != 7 {
		t.Fatalf("冲突响应应带正在执行的编号: %+v", payload)
	}

	statusSnapshot := service.AutoAllStatus()
	if running, _ := statusSnapshot["running"].(bool); !running {
		t.Fatalf("冲突不应改变运行状态: %+v", statusSnapshot)
	}
	if _, exists := statusSnapshot["finished_at"]; exists {
		t.Fatalf("仍在执行时不应给出结束时间: %+v", statusSnapshot)
	}
}

// TestAutoAllStatusReportsProgressAndResult 锁定进度快照的字段：
// running / seq / progress / total / 起止时间 / 上一次结果。
func TestAutoAllStatusReportsProgressAndResult(t *testing.T) {
	service := newAutoAllService()
	service.autoAll.seq = 3
	service.autoAll.started = time.Date(2026, 10, 31, 12, 0, 0, 0, checkinZone)
	service.autoAll.finished = time.Date(2026, 10, 31, 12, 5, 0, 0, checkinZone)
	service.autoAll.progress = 2
	service.autoAll.total = 2
	service.autoAll.payload = map[string]any{"ok": true, "accounts": 2}

	got := service.AutoAllStatus()
	if running, _ := got["running"].(bool); running {
		t.Fatalf("已结束的任务不应报 running: %+v", got)
	}
	if seq, _ := got["seq"].(int64); seq != 3 {
		t.Fatalf("seq = %v, want 3", got["seq"])
	}
	if started, _ := got["started_at"].(string); started == "" {
		t.Fatalf("缺少 started_at: %+v", got)
	}
	if finished, _ := got["finished_at"].(string); finished == "" {
		t.Fatalf("缺少 finished_at: %+v", got)
	}
	result, okResult := got["result"].(map[string]any)
	if !okResult || result["accounts"] != 2 {
		t.Fatalf("应带回上一次结果: %+v", got)
	}
}
