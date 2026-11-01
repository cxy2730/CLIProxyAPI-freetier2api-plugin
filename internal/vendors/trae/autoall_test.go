package trae

import "testing"

func TestSummarizeUGForAutoTask(t *testing.T) {
	cases := []struct {
		name       string
		results    []UGResult
		wantStatus string
	}{
		{
			name:       "无入口",
			results:    nil,
			wantStatus: "skipped",
		},
		{
			name: "领到即 done",
			results: []UGResult{
				{Status: UGClaimed, Message: "活动中心 +50"},
				{Status: UGAlready, Message: "裂变已领"},
			},
			wantStatus: "done",
		},
		{
			name: "全已领是 skipped",
			results: []UGResult{
				{Status: UGAlready, Message: "活动中心已领"},
				{Status: UGAlready, Message: "裂变已领"},
			},
			wantStatus: "skipped",
		},
		{
			name: "全失败才是 error",
			results: []UGResult{
				{Status: UGFailed, Message: "上游 500"},
				{Status: UGFailed, Message: "上游 502"},
			},
			wantStatus: "error",
		},
		{
			name: "失败但已有已领 -> 不算 error",
			results: []UGResult{
				{Status: UGFailed, Message: "上游 500"},
				{Status: UGAlready, Message: "已领"},
			},
			wantStatus: "skipped",
		},
		{
			name: "只有不可领项 -> skipped",
			results: []UGResult{
				{Status: UGUnavailable, Message: "活动未开始"},
			},
			wantStatus: "skipped",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, message := summarizeUGForAutoTask(testCase.results)
			if status != testCase.wantStatus {
				t.Fatalf("status = %q, want %q (message=%q)", status, testCase.wantStatus, message)
			}
			if message == "" {
				t.Fatalf("message 不应为空")
			}
		})
	}
}
