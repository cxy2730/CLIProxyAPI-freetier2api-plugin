package qoder

import (
	"testing"

	"freetier2api-plugin/internal/auth"
)

// TestOrderedQuotaFamiliesPutsUsableBucketFirst 锁定「页头数字由谁提供」。
//
// 宿主 auth.Totals 取第一个有容量的族，而「有容量」包含
// 「total>0、remaining==0」（额度合法用完）——所以已过期的桶若排在前面，
// 页头会永远显示 0，账号明明还有额度也看着像没额度。
func TestOrderedQuotaFamiliesPutsUsableBucketFirst(t *testing.T) {
	cases := []struct {
		name      string
		quota     *Quota
		wantFirst string
	}{
		{
			// issue #70 的真实形态：套餐用尽、个人拓展包过期、额度在组织资源包。
			name: "企业账号：组织资源包排到最前",
			quota: &Quota{
				UserQuota:  &QuotaBucket{Used: 3000, Total: 3000, Remaining: 0},
				AddonQuota: &QuotaBucket{Used: 3000, Total: 3000, Remaining: 0},
				OrgPackage: &QuotaBucket{Used: 1000, Total: 3000, Remaining: 2000},
			},
			wantFirst: "org_quota",
		},
		{
			name: "个人 Pro：套餐还有余额时仍是套餐在前",
			quota: &Quota{
				UserQuota:  &QuotaBucket{Used: 100, Total: 1000, Remaining: 900},
				AddonQuota: &QuotaBucket{Used: 50, Total: 100, Remaining: 50},
			},
			wantFirst: "user_quota",
		},
		{
			// Free 计划：套餐是合法的 0/0，不能因为它排第一就把页头锁在 0/0。
			name: "Free 计划：0/0 的套餐让位给拓展包",
			quota: &Quota{
				UserQuota:  &QuotaBucket{},
				AddonQuota: &QuotaBucket{Used: 150, Total: 200, Remaining: 50},
			},
			wantFirst: "addon_quota",
		},
		{
			// 三桶都没余额时仍要有页头：回落套餐，显示「0 / 3000」才是事实。
			name: "全部用尽：回落套餐",
			quota: &Quota{
				UserQuota:  &QuotaBucket{Used: 3000, Total: 3000, Remaining: 0},
				AddonQuota: &QuotaBucket{},
			},
			wantFirst: "user_quota",
		},
		{
			name:      "全部 0/0：回落套餐",
			quota:     &Quota{UserQuota: &QuotaBucket{}, AddonQuota: &QuotaBucket{}},
			wantFirst: "user_quota",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ordered := orderedQuotaFamilies(testCase.quota)
			if len(ordered) == 0 {
				t.Fatal("orderedQuotaFamilies 返回空，页头会没有数字")
			}
			if ordered[0].prefix != testCase.wantFirst {
				t.Fatalf("首个族 = %s，want %s", ordered[0].prefix, testCase.wantFirst)
			}
		})
	}
}

// TestExtractOrgPackageDiscoversUnknownBucket 锁「结构发现」而不是写死键名：
// 企业账号的资源包桶名没有实测样本，只能按形状认。
func TestExtractOrgPackageDiscoversUnknownBucket(t *testing.T) {
	payload := map[string]any{
		"plan":            "Enterprise",
		"expiresAt":       float64(1790000000000),
		"isQuotaExceeded": false,
		"userQuota":       map[string]any{"total": float64(3000), "remaining": float64(0), "used": float64(3000)},
		"addOnQuota":      map[string]any{"total": float64(3000), "remaining": float64(0), "used": float64(3000)},
		"orgResourcePackage": map[string]any{
			"total": float64(3000), "remaining": float64(2000), "used": float64(1000),
			"resetTime": "2026-12-01T00:00:00Z",
		},
		"outerProviders": []any{"a"},
	}
	org := extractOrgPackage(payload)
	if org == nil {
		t.Fatal("没识别出资源包")
	}
	if org.Total != 3000 || org.Remaining != 2000 || org.Used != 1000 {
		t.Fatalf("资源包读数 = %+v，want 3000/2000/1000", org)
	}
	if org.ResetTime != "2026-12-01T00:00:00Z" {
		t.Fatalf("resetTime 没读进来：%q", org.ResetTime)
	}

	// 换个键名与内部字段名（cap/available）也要认。
	renamed := map[string]any{
		"userQuota":  map[string]any{"total": float64(3000), "remaining": float64(3000)},
		"addOnQuota": map[string]any{"total": float64(0), "remaining": float64(0)},
		"teamWallet": map[string]any{"cap": float64(800), "available": float64(120)},
	}
	if got := extractOrgPackage(renamed); got == nil || got.Total != 800 || got.Remaining != 120 || got.Used != 680 {
		t.Fatalf("换名后识别 = %+v，want 800/120/680", got)
	}
}

// TestExtractOrgPackageIgnoresNonBucketObjects 防误认：
// 只有总额没有剩余、或根本不是对象的，都不算额度桶。
func TestExtractOrgPackageIgnoresNonBucketObjects(t *testing.T) {
	cases := map[string]map[string]any{
		"只有总额":  {"something": map[string]any{"total": float64(100)}},
		"只有剩余":  {"something": map[string]any{"remaining": float64(100)}},
		"非数值":   {"something": map[string]any{"total": "100", "remaining": "50"}},
		"数组":    {"outerProviders": []any{map[string]any{"total": float64(1), "remaining": float64(1)}}},
		"全是已知桶": {"userQuota": map[string]any{"total": float64(1), "remaining": float64(1)}, "addOnQuota": map[string]any{"total": float64(2), "remaining": float64(2)}},
	}
	for name, payload := range cases {
		if got := extractOrgPackage(payload); got != nil {
			t.Fatalf("%s：误认成资源包 %+v", name, got)
		}
	}
}

// TestBuildQuotaResponseHeadlineMatchesUsableBucket 走完整链路：
// BuildQuotaResponse → auth.Totals，断言页头数字就是账号真实可用的额度。
func TestBuildQuotaResponseHeadlineMatchesUsableBucket(t *testing.T) {
	enterprise := BuildQuotaResponse(&Quota{
		Plan:       "Enterprise",
		UserQuota:  &QuotaBucket{Used: 3000, Total: 3000, Remaining: 0},
		AddonQuota: &QuotaBucket{Used: 3000, Total: 3000, Remaining: 0},
		OrgPackage: &QuotaBucket{Used: 1000, Total: 3000, Remaining: 2000},
	})
	if got := auth.Totals(&enterprise); got != (auth.QuotaTotals{Remaining: 2000, Total: 3000}) {
		t.Fatalf("企业账号页头 = %+v，want 2000/3000", got)
	}
	if len(enterprise.Groups) != 3 {
		t.Fatalf("三个桶都要有分组明细，得到 %d 个", len(enterprise.Groups))
	}
	// 三个桶的数字一个都不能丢，只是顺序变了。
	if len(enterprise.Summary) != 9 {
		t.Fatalf("Summary 应有 9 条指标（3 桶 × 3 项），得到 %d", len(enterprise.Summary))
	}

	free := BuildQuotaResponse(&Quota{
		Plan:       "Free",
		UserQuota:  &QuotaBucket{},
		AddonQuota: &QuotaBucket{Used: 150, Total: 200, Remaining: 50},
	})
	if got := auth.Totals(&free); got != (auth.QuotaTotals{Remaining: 50, Total: 200}) {
		t.Fatalf("Free 计划页头 = %+v，want 50/200", got)
	}
}
