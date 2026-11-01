package admin

import (
	"testing"

	"freetier2api-plugin/internal/core"
)

// 确定类失败不能拿上次读数顶：硬额度耗尽、凭证失效这类不会自己好转，
// 保留上次的正常读数等于把「额度已经没了」显示成还有额度。
func TestKeepLastQuotaSkipsDefinitiveFailure(t *testing.T) {
	previous := &quotaResult{OK: true, Remain: 900, Total: 1000}

	if _, ok := keepLastQuota(previous, &core.Failure{Kind: core.FailureKindHardCredit}); ok {
		t.Fatal("硬额度耗尽不该保留上次读数")
	}
	if _, ok := keepLastQuota(previous, &core.Failure{Kind: core.FailureKindCredential}); ok {
		t.Fatal("凭证失效不该保留上次读数")
	}
	kept, ok := keepLastQuota(previous, &core.Failure{Kind: core.FailureKindRateLimit})
	if !ok {
		t.Fatal("限流应保留上次读数")
	}
	if !kept.Stale || kept.Remain != 900 || kept.Total != 1000 {
		t.Fatalf("kept = %+v", kept)
	}
	if kept.StaleReason == "" {
		t.Fatal("保留上次读数时必须给出原因，否则页面只能显示「非实时」而说不出为什么")
	}
}

// 上游这次没取到、之前也没取到过：没有任何可保留的读数，必须如实报错。
func TestKeepLastQuotaWithoutPrevious(t *testing.T) {
	if _, ok := keepLastQuota(nil, &core.Failure{Kind: core.FailureKindRateLimit}); ok {
		t.Fatal("没有上次读数时不该伪造一份")
	}
}
