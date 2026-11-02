package workbuddy

import (
	"strings"
	"testing"

	"freetier2api-plugin/internal/core"
)

func TestConfigureAppliesUADefaults(t *testing.T) {
	dir := t.TempDir()
	rt := newRuntime(core.Env{})
	rt.configure(core.Settings{
		EnabledRealms: []string{"cn"},
		MachineSalt:   "salt-1",
		StateDir:      dir,
	}, map[string]string{
		"workbuddy_client_version": "9.9.9",
	})

	cfg := *rt.cfg.Load()
	if cfg.clientVersion != "9.9.9" {
		t.Fatalf("client version = %q", cfg.clientVersion)
	}
	if *rt.salt.Load() != "salt-1" {
		t.Fatalf("install salt = %q", *rt.salt.Load())
	}
	if len(cfg.enabledRealms) != 1 || cfg.enabledRealms[0] != "cn" {
		t.Fatalf("realms = %#v", cfg.enabledRealms)
	}

}

// TestWorkBuddyRuntimeIsolated 验证两个运行时的提示词、档位表与机器盐互不污染。
func TestWorkBuddyRuntimeIsolated(t *testing.T) {
	first := newRuntime(core.Env{})
	second := newRuntime(core.Env{})

	first.configure(core.Settings{MachineSalt: "salt-first"}, map[string]string{
		"workbuddy_client_version": "9.9.9",
	})
	second.configure(core.Settings{MachineSalt: "salt-second"}, map[string]string{
		"workbuddy_client_version": "8.8.8",
	})
	first.setEffortTables(
		map[string]string{"glm-5.2": "high"},
		map[string][]string{"glm-5.2": {"low", "high"}},
	)
	second.setEffortTables(
		map[string]string{"glm-5.2": "low"},
		map[string][]string{"glm-5.2": {"low"}},
	)

	if got := *first.salt.Load(); got != "salt-first" {
		t.Fatalf("first salt = %q", got)
	}
	if got := *second.salt.Load(); got != "salt-second" {
		t.Fatalf("second salt = %q", got)
	}
	if got := first.cfg.Load().clientVersion; got != "9.9.9" {
		t.Fatalf("first client version = %q", got)
	}
	if got := second.cfg.Load().clientVersion; got != "8.8.8" {
		t.Fatalf("second client version = %q", got)
	}
	defaults, supported := first.effortTables()
	if defaults["glm-5.2"] != "high" || len(supported["glm-5.2"]) != 2 {
		t.Fatalf("first effort tables = %#v / %#v", defaults, supported)
	}
	defaults, supported = second.effortTables()
	if defaults["glm-5.2"] != "low" || len(supported["glm-5.2"]) != 1 {
		t.Fatalf("second effort tables = %#v / %#v", defaults, supported)
	}
}

// TestDeriveIDStabilityAndIsolation 验证设备指纹的稳定性与隔离性。
func TestDeriveIDStabilityAndIsolation(t *testing.T) {
	rt := newRuntime(core.Env{})
	setSalt := func(s string) { value := s; rt.salt.Store(&value) }
	setSalt("salt-a")
	first := rt.deriveID("uid-1", "machine")
	second := rt.deriveID("uid-1", "machine")
	if first != second {
		t.Fatal("deriveID must be stable for the same input")
	}
	if len(first) != 36 {
		t.Fatalf("deriveID length = %d, want 36 (official device id shape)", len(first))
	}
	if rt.deriveID("uid-2", "machine") == first {
		t.Fatal("different accounts must get different fingerprints")
	}
	// 换机器盐 = 换设备：指纹必须整体漂移。
	setSalt("salt-b")
	if rt.deriveID("uid-1", "machine") == first {
		t.Fatal("changing the install salt must change fingerprints")
	}
}

// TestBuildCacheKeyIsolatesAccounts 验证缓存键的账号隔离。
//
// uid 前缀是硬隔离因子：跨账号复用同一 cache key 会让上游命中
// 错账号的前缀缓存，泄露对方对话内容。
func TestBuildCacheKeyIsolatesAccounts(t *testing.T) {
	keyA := buildCacheKey("account-aaaa1111", "conv-1")
	keyB := buildCacheKey("account-bbbb2222", "conv-1")
	if keyA == keyB {
		t.Fatal("different accounts must produce different cache keys")
	}
	if !strings.HasPrefix(keyA, "wb2a-account-") {
		t.Fatalf("cache key should carry the uid prefix: %q", keyA)
	}
	if buildCacheKey("account-aaaa1111", "conv-1") != keyA {
		t.Fatal("cache key must be deterministic")
	}
}

// TestCheckinUnsupportedReasonEnterprise 企业账号没有签到活动。
//
// 企业账号打 daily-checkin 恒得到 400 code 10001；如果仍按「支持签到」处理，
// 自动任务每天会给它记一条失败日志。判据是凭证里的企业租户 id。
func TestCheckinUnsupportedReasonEnterprise(t *testing.T) {
	personal := testTaskCredential("uid-personal")
	if reason := checkinUnsupportedReason(personal); reason != "" {
		t.Fatalf("个人账号应支持签到，得到 reason=%q", reason)
	}

	enterprise := testTaskCredential("uid-enterprise")
	enterprise.EnterpriseID = "fysxmvfpz6dc"
	reason := checkinUnsupportedReason(enterprise)
	if !strings.Contains(reason, "企业账号") {
		t.Fatalf("企业账号的不可签到原因必须可读，得到 %q", reason)
	}

	// 国际版走每日活跃奖励，与国内版签到端点无关。
	global := testGlobalCredential("uid-global-enterprise")
	global.EnterpriseID = "ent-global"
	if reason := checkinUnsupportedReason(global); reason != "" {
		t.Fatalf("国际版活跃奖励不受国内企业判定影响，得到 reason=%q", reason)
	}
}
