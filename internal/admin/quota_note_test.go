package admin

// 本文件锁住「已领取但额度为空」的说明文案。
//
// 说明原来由 zcode 供应商塞进宿主的 ClaimStatus.Note，等于往宿主契约里加插件
// 专用字段；现在由 admin 统一生成，供应商只管取数（#71 第二轮复审 P2-3）。

import (
	"testing"

	"freetier2api-plugin/cpasdk/pluginapi"
)

func TestClaimNoteExplainsEmptyQuota(t *testing.T) {
	claim := &pluginapi.ClaimStatus{Available: 1, Claimable: 0}
	note := claimNote(claim, quotaResult{})
	if note == "" {
		t.Fatal("已领取但没有额度明细时应当给一句说明")
	}
	if note != "已领取，上游暂未返回额度明细；活动额度通常要几分钟才生效" {
		t.Fatalf("说明文案变了：%q", note)
	}
}

func TestClaimNotePointsAtClaimablePlan(t *testing.T) {
	// 有可领取的套餐时，「领取后才会生成额度」比「已领取」准确。
	claim := &pluginapi.ClaimStatus{Available: 0, Claimable: 1}
	note := claimNote(claim, quotaResult{})
	if note != "有可领取的套餐，领取后才会生成额度" {
		t.Fatalf("说明文案变了：%q", note)
	}
}

func TestClaimNoteStaysSilentWhenQuotaExists(t *testing.T) {
	claim := &pluginapi.ClaimStatus{Available: 1}
	// 有额度明细就不该再解释「为什么没有额度」。
	if note := claimNote(claim, quotaResult{Metrics: []pluginapi.QuotaMetric{{Key: "daily"}}}); note != "" {
		t.Fatalf("有明细时不应出说明，得到 %q", note)
	}
	// 有总额同样算有数据。
	if note := claimNote(claim, quotaResult{HasTotal: true, Total: 100}); note != "" {
		t.Fatalf("有总额时不应出说明，得到 %q", note)
	}
	// 上游报了总额键但总额为 0：仍然算「没有额度数据」，需要说明。
	if note := claimNote(claim, quotaResult{HasTotal: true, Total: 0}); note == "" {
		t.Fatal("总额为 0 且无明细时应当给说明")
	}
}

func TestClaimNoteSkipsFailedAndMissingClaim(t *testing.T) {
	if note := claimNote(nil, quotaResult{}); note != "" {
		t.Fatalf("没有领取状态时不该编说明，得到 %q", note)
	}
	// 取数失败时 Error 已经说明原因，再叠一句只会让页面显示两条。
	failed := &pluginapi.ClaimStatus{Error: "unauthorized: token expired"}
	if note := claimNote(failed, quotaResult{}); note != "" {
		t.Fatalf("失败时不该再加说明，得到 %q", note)
	}
}
