package admin

import "testing"

func TestDecodeBlockListsAndUnknownKeys(t *testing.T) {
	raw := []byte(`
enabled: true
priority: 1
enabled_realms:
  - cn
  - global
extra_models: [glm-5.2, qwen-max]
disabled_models:
  - cn:glm-5.2
  - qwen-3.5-plus
auto_checkin: false
vendor_specific_key: whatever
`)
	cfg, err := DecodePluginConfig(raw, DefaultPluginConfig())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cfg.EnabledRealms) != 2 || cfg.EnabledRealms[0] != "cn" || cfg.EnabledRealms[1] != "global" {
		t.Fatalf("realms = %#v", cfg.EnabledRealms)
	}
	if len(cfg.ExtraModels) != 2 || cfg.ExtraModels[0] != "glm-5.2" {
		t.Fatalf("extra models = %#v", cfg.ExtraModels)
	}
	if len(cfg.DisabledModels) != 2 {
		t.Fatalf("disabled models = %#v", cfg.DisabledModels)
	}
	if cfg.AutoCheckin {
		t.Fatal("auto_checkin should be false")
	}
	if cfg.Raw["vendor_specific_key"] != "whatever" {
		t.Fatalf("raw map did not keep unknown key: %#v", cfg.Raw)
	}
}

func TestDecodeValidation(t *testing.T) {
	for _, raw := range []string{
		"enabled_realms: mars\n",
		"log_level: verbose\n",
		"auto_checkin_at: 25:00\n",
	} {
		if _, err := DecodePluginConfig([]byte(raw), DefaultPluginConfig()); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}

func TestParseClock(t *testing.T) {
	hour, minute, err := ParseClock("09:05")
	if err != nil || hour != 9 || minute != 5 {
		t.Fatalf("ParseClock = %d,%d,%v", hour, minute, err)
	}
	for _, invalid := range []string{"", "9", "24:00", "09:60", "09:05:00"} {
		if _, _, err := ParseClock(invalid); err == nil {
			t.Fatalf("ParseClock(%q) 应报错", invalid)
		}
	}
}

func TestDefaultStateDirHonoursEnv(t *testing.T) {
	t.Setenv(StateDirEnvOverride, "/tmp/custom-state")
	if got := DefaultStateDir(); got != "/tmp/custom-state" {
		t.Fatalf("state dir = %q", got)
	}
}

func TestDecodeCheckinHistoryLimit(t *testing.T) {
	if got := DefaultPluginConfig().CheckinHistoryLimit; got != 30 {
		t.Fatalf("默认签到历史条数 = %d, 期望 30", got)
	}
	cfg, err := DecodePluginConfig([]byte("checkin_history_limit: 7\n"), DefaultPluginConfig())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.CheckinHistoryLimit != 7 {
		t.Fatalf("checkin_history_limit = %d, 期望 7", cfg.CheckinHistoryLimit)
	}
	if _, errBad := DecodePluginConfig([]byte("checkin_history_limit: lots\n"), DefaultPluginConfig()); errBad == nil {
		t.Fatal("非数字的 checkin_history_limit 必须报错，而不是静默回落到默认值")
	}
}
