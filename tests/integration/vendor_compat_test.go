package tests

// 本文件锁死对外契约：vendor 实例清单（ID/名称/区域）与供应商专属配置键。
//
// 这些是用户可见的红线（凭证文件名前缀、控制台下拉项、配置键语义），
// 任何重构都不应改变它们；一旦变了，这里会先失败。

import (
	"testing"

	"freetier2api-plugin/internal/core"
)

// TestVendorRegistryCompatibility 锁定 15 个供应商实例的对外身份与顺序。
func TestVendorRegistryCompatibility(t *testing.T) {
	installFakeHost(t)
	setupTestPlugin(t)
	want := []struct {
		id     string
		name   string
		region string
	}{
		{"autoclawcn", "AutoClaw 国内版", "cn"},
		{"autoclawglobal", "AutoClaw 国际版", "global"},
		{"cline", "Cline", ""},
		{"codearts", "CodeArts", "cn"},
		{"minimaxcodecn", "MiniMax Code 国内版", "cn"},
		{"minimaxcodeglobal", "MiniMax Code 国际版", "global"},
		{"opencodezen", "OpenCode ZEN", ""},
		{"qodercn", "Qoder 国内版", "cn"},
		{"qoderglobal", "Qoder 国际版", "global"},
		{"raccoon", "小浣熊", ""},
		{"traecn", "Trae 国内版", "cn"},
		{"traeglobal", "Trae 国际版", "global"},
		{"workbuddycn", "WorkBuddy 国内版", "cn"},
		{"workbuddyglobal", "WorkBuddy 国际版", "global"},
		{"zcodecn", "ZCode 国内版", "cn"},
		{"zcodeglobal", "ZCode 国际版", "global"},
	}
	vendors := testApp.Registry().Vendors()
	if len(vendors) != len(want) {
		t.Fatalf("vendor count = %d, want %d", len(vendors), len(want))
	}
	for index, expected := range want {
		vendor := vendors[index]
		if vendor.ID() != expected.id || vendor.Name() != expected.name || vendor.Region() != expected.region {
			t.Fatalf("vendor[%d] = %q/%q/%q, want %q/%q/%q", index,
				vendor.ID(), vendor.Name(), vendor.Region(), expected.id, expected.name, expected.region)
		}
		if vendor.ModelPrefix() != "" {
			t.Fatalf("vendor %s model prefix = %q, want empty", vendor.ID(), vendor.ModelPrefix())
		}
	}
}

// TestVendorCapabilities 锁定每个供应商实现的能力集合。
//
// 能力由「是否实现可选接口」表达：任何一家多实现/漏实现都会改变控制台入口、
// 额度/签到行为或任务调度，必须显式确认。
// isPlatformTaskKind 报告该任务族是否由平台专用路由触发（不需要 spec.Route）。
func isPlatformTaskKind(kind string) bool {
	return kind == core.TaskKindKeepalive || kind == core.TaskKindCheckin
}

func TestVendorCapabilities(t *testing.T) {
	installFakeHost(t)
	setupTestPlugin(t)
	type want struct {
		login     bool
		refresh   bool
		quota     bool
		checkin   bool
		tasks     bool
		models    bool // 自定义 ModelsForAuth（否则回落 StaticModels）
		merge     bool
		configure bool
		console   bool
		credits   bool
		authMode  string
	}
	cases := map[string]want{
		"autoclawcn":        {credits: true, refresh: true, quota: true, checkin: true, models: true, merge: true, configure: true, authMode: "oauth"},
		"autoclawglobal":    {credits: true, login: true, refresh: true, quota: true, checkin: true, models: true, merge: true, configure: true, authMode: "oauth"},
		"cline":             {credits: true, login: true, refresh: true, merge: true, configure: true, authMode: "oauth"},
		"codearts":          {credits: true, login: true, refresh: true, quota: true, checkin: true, models: true, merge: true, configure: true},
		"minimaxcodecn":     {login: true, refresh: true, quota: true, checkin: true, models: true, merge: true, configure: true},
		"minimaxcodeglobal": {login: true, refresh: true, quota: true, checkin: true, models: true, merge: true, configure: true},
		"opencodezen":       {credits: true, refresh: true, models: true, merge: true, configure: true, authMode: "apikey"},
		"qodercn":           {credits: true, login: true, refresh: true, quota: true, checkin: true, models: true, merge: true, configure: true},
		"qoderglobal":       {credits: true, login: true, refresh: true, quota: true, checkin: true, models: true, merge: true, configure: true},
		"raccoon":           {credits: true, login: true, refresh: true, quota: true, checkin: true, models: true, merge: true, configure: true},
		"traecn":            {credits: true, login: true, refresh: true, quota: true, checkin: true, tasks: true, merge: true, configure: true},
		// tasks 是「接口能力」而不是「本区域有族」：国际版实现了 TaskCatalogProvider
		// 但 TaskSpecs() 返回空（活动福利只在国内版），与 checkin 同一种约定。
		"traeglobal":      {credits: true, login: true, refresh: true, quota: true, checkin: true, tasks: true, merge: true, configure: true},
		"workbuddycn":     {credits: true, login: true, refresh: true, quota: true, checkin: true, tasks: true, models: true, merge: true, configure: true, console: true},
		"workbuddyglobal": {credits: true, login: true, refresh: true, quota: true, checkin: true, tasks: true, models: true, merge: true, configure: true, console: true},
		"zcodecn":         {login: true, refresh: true, quota: true, merge: true, configure: true, authMode: "oauth"},
		"zcodeglobal":     {login: true, refresh: true, quota: true, merge: true, configure: true, authMode: "oauth"},
	}
	for _, vendor := range testApp.Registry().Vendors() {
		expected, okCase := cases[vendor.ID()]
		if !okCase {
			t.Fatalf("unexpected vendor %q in registry", vendor.ID())
		}
		_, hasLogin := core.As[core.LoginProvider](vendor)
		_, hasRefresh := core.As[core.Refresher](vendor)
		_, hasQuota := core.As[core.QuotaProvider](vendor)
		_, hasCheckin := core.As[core.CheckinProvider](vendor)
		_, hasTasks := core.As[core.TaskCatalogProvider](vendor)
		_, hasModels := core.As[core.ModelsProvider](vendor)
		_, hasMerge := core.As[core.StorageMerger](vendor)
		_, hasConfigure := core.As[core.Configurable](vendor)
		_, hasConsole := core.As[core.ConsoleModelProvider](vendor)
		_, hasCredits := core.As[core.CreditsProvider](vendor)
		authMode := ""
		if reporter, okReporter := core.As[core.AuthModeReporter](vendor); okReporter {
			authMode = reporter.AuthMode()
		}
		got := want{
			login: hasLogin, refresh: hasRefresh, quota: hasQuota, checkin: hasCheckin,
			tasks: hasTasks, models: hasModels, merge: hasMerge, configure: hasConfigure,
			console: hasConsole, credits: hasCredits, authMode: authMode,
		}
		if got != expected {
			t.Fatalf("vendor %s capabilities = %+v, want %+v", vendor.ID(), got, expected)
		}
		// 支持签到必须有接口；有接口但本区域不支持是合法的（如 MiniMax 国内版）。
		if core.SupportsCheckin(vendor) && !hasCheckin {
			t.Fatalf("vendor %s reports checkin without implementing CheckinProvider", vendor.ID())
		}
		// 任务族同理允许区域空集；但声明出来的族必须可用：Kind 非空，手动族
		// 必须带 Route（宿主只给非空 Route 注册管理路由）。
		if catalog, okCatalog := core.As[core.TaskCatalogProvider](vendor); okCatalog {
			for _, spec := range catalog.TaskSpecs() {
				if spec.Kind == "" {
					t.Fatalf("vendor %s declares a task spec without kind", vendor.ID())
				}
				// 平台级任务族（保活）由宿主的专用路由触发，不走 spec.Route。
				if spec.Manual && spec.Route == "" && !isPlatformTaskKind(spec.Kind) {
					t.Fatalf("vendor %s 手动任务族 %q 缺少 Route，控制台不会有触发入口", vendor.ID(), spec.Kind)
				}
			}
		}
	}
}
