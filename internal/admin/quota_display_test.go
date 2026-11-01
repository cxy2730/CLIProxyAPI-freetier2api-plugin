package admin

import (
	"context"
	"testing"

	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/core"
)

// quotaVendor / plainVendor 用于验证「能力标志」与「查询结果」是两件事。
type quotaCapableVendor struct{ fakeVendor }

func (v *quotaCapableVendor) Quota(context.Context, *core.Credential) (*pluginapi.QuotaFetchResponse, error) {
	return &pluginapi.QuotaFetchResponse{}, nil
}

// TestSupportsQuotaComesFromCapability 锁定额度列的状态判定来源。
//
// 页面过去靠「结果里有没有这条」反推状态，于是「上游没有额度接口」（OpenCode
// ZEN）与「还没查」无法区分，账号会永久停在「查询中…」。能力必须来自接口断言。
func TestSupportsQuotaComesFromCapability(t *testing.T) {
	plain := &fakeVendor{Meta: core.Meta{VendorID: "plain"}}
	if supportsQuota(plain) {
		t.Fatal("vendor without QuotaProvider must report supports_quota=false")
	}
	capable := &quotaCapableVendor{fakeVendor{Meta: core.Meta{VendorID: "capable"}}}
	if !supportsQuota(capable) {
		t.Fatal("vendor implementing QuotaProvider must report supports_quota=true")
	}
}

// TestQuotaCacheKeyIsolatedByVendorAndAccount 锁定额度缓存键的唯一性。
//
// 键里必须同时含供应商与账号身份：过去把 Name / Label / AuthIndex 也当键，
// 跨供应商同名就会互相串值（OpenCode ZEN 显示 WorkBuddy 额度的事故）。
func TestQuotaCacheKeyIsolatedByVendorAndAccount(t *testing.T) {
	if quotaCacheKey("workbuddycn", "a", "") == quotaCacheKey("opencodezen", "a", "") {
		t.Fatal("cache key must include vendor: same account id across vendors must not collide")
	}
	if quotaCacheKey("workbuddycn", "a", "") == quotaCacheKey("workbuddycn", "b", "") {
		t.Fatal("cache key must include account identity")
	}
	if quotaCacheKey("", "a", "") != "" || quotaCacheKey("v", "", "") != "" {
		t.Fatal("incomplete identity must produce no key")
	}
	if quotaCacheKey("v", "", "auth-1") == "" {
		t.Fatal("AuthID must serve as a fallback identity when FileID is empty")
	}
}

// TestQuotaCacheIsolatedByVendor 锁定读取侧：同名账号跨供应商不共享额度。
func TestQuotaCacheIsolatedByVendor(t *testing.T) {
	s := &Service{quotaCache: map[string]quotaResult{}}
	s.cacheQuotaResult(quotaResult{VendorID: "workbuddycn", UID: "acc-1", Remain: 10, Total: 20})
	if q := s.getCachedQuota("opencodezen", "acc-1", "auth-1"); q != nil {
		t.Fatalf("must not read another vendor's quota: %+v", q)
	}
	if q := s.getCachedQuota("workbuddycn", "acc-1", "auth-1"); q == nil || q.Remain != 10 {
		t.Fatalf("own quota must be readable, got %+v", q)
	}
}
