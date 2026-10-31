package tasks

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// autoAllTimeout 是后台「一键完成」的总时限。
//
// 一键完成按账号串行跑（上游按账号判风控，不能并发），账号多、上游慢时
// 很容易超过管理请求的超时。以前它是同步接口：请求超时被取消，账号却已经
// 跑了一半，页面只能看到一个失败。现在改成后台执行 + 页面轮询进度，
// 给它一个宽松的总时限。
const autoAllTimeout = 30 * time.Minute

// autoAllJob 是后台「一键完成」的进度快照。
type autoAllJob struct {
	mu       sync.Mutex
	running  bool
	seq      int64
	started  time.Time
	finished time.Time
	total    int
	progress int
	payload  map[string]any
}

// StartAutoAll 在后台执行一键完成，立即返回任务编号；进度用 AutoAllStatus 轮询。
//
// 同一时刻只允许一个后台任务：重复点击返回 409 和正在执行的那个编号，
// 前端据此直接接着轮询，不会起两份任务把上游打成并发。
func (s *Service) StartAutoAll(callbackID string, targets []string) (map[string]any, int) {
	job := s.autoAll
	job.mu.Lock()
	if job.running {
		seq := job.seq
		job.mu.Unlock()
		return map[string]any{"ok": false, "error": "一键完成正在执行中", "seq": seq}, http.StatusConflict
	}
	job.running = true
	job.seq++
	job.started = s.deps.Now()
	job.finished = time.Time{}
	job.total = 0
	job.progress = 0
	job.payload = nil
	seq := job.seq
	job.mu.Unlock()

	s.trackJob(func(parent context.Context) {
		ctx, cancel := context.WithTimeout(parent, autoAllTimeout)
		defer cancel()
		payload := s.runAutoAllTargets(ctx, callbackID, targets, func(done, total int) {
			job.mu.Lock()
			if job.seq == seq {
				job.progress = done
				job.total = total
			}
			job.mu.Unlock()
		})
		job.mu.Lock()
		if job.seq == seq {
			job.running = false
			job.finished = s.deps.Now()
			job.payload = payload
		}
		job.mu.Unlock()
	})
	return map[string]any{"ok": true, "started": true, "seq": seq}, http.StatusOK
}

// AutoAllStatus 返回一键完成的后台进度；空闲时 result 是上一次的结果。
func (s *Service) AutoAllStatus() map[string]any {
	job := s.autoAll
	job.mu.Lock()
	defer job.mu.Unlock()
	out := map[string]any{
		"running":  job.running,
		"seq":      job.seq,
		"progress": job.progress,
		"total":    job.total,
	}
	if !job.started.IsZero() {
		out["started_at"] = job.started.Format(time.RFC3339)
	}
	if !job.finished.IsZero() {
		out["finished_at"] = job.finished.Format(time.RFC3339)
	}
	if job.payload != nil {
		out["result"] = job.payload
	}
	return out
}
