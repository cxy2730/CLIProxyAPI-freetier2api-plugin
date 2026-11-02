package tests

import (
	"context"
	"encoding/json"
	"testing"

	"freetier2api-plugin/cpasdk/pluginabi"
	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/app"
	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/state"
)

// TestAppsHaveIsolatedRegistryAndState 验证 App 实例之间不共享注册表与状态。
func TestAppsHaveIsolatedRegistryAndState(t *testing.T) {
	first, errFirst := app.New(app.Options{Version: "a"})
	if errFirst != nil {
		t.Fatalf("new first: %v", errFirst)
	}
	second, errSecond := app.New(app.Options{Version: "b", Factories: app.BuiltinFactories()})
	if errSecond != nil {
		t.Fatalf("new second: %v", errSecond)
	}
	if len(first.Registry().Vendors()) != 0 {
		t.Fatalf("first app registry should be empty")
	}
	if len(second.Registry().Vendors()) == 0 {
		t.Fatalf("second app registry should contain builtins")
	}

	cfgFirst := app.DefaultConfig()
	cfgFirst.StateDir = t.TempDir()
	if errApply := first.ApplyConfig(cfgFirst); errApply != nil {
		t.Fatalf("apply first: %v", errApply)
	}
	cfgSecond := app.DefaultConfig()
	cfgSecond.StateDir = t.TempDir()
	if errApply := second.ApplyConfig(cfgSecond); errApply != nil {
		t.Fatalf("apply second: %v", errApply)
	}
	first.MutateState(func(state *state.State) {
		state.DisabledModels = []string{"vendor:model"}
	})
	if got := second.SnapshotState().DisabledModels; len(got) != 0 {
		t.Fatalf("second app saw first app state: %#v", got)
	}
}

// TestAppsIsolateLoginSessions 验证两个 App 的登录会话互不可见。
func TestAppsIsolateLoginSessions(t *testing.T) {
	first, errFirst := app.New(app.Options{Version: "a", Factories: app.BuiltinFactories()})
	if errFirst != nil {
		t.Fatalf("new first: %v", errFirst)
	}
	second, errSecond := app.New(app.Options{Version: "b", Factories: app.BuiltinFactories()})
	if errSecond != nil {
		t.Fatalf("new second: %v", errSecond)
	}
	for _, instance := range []*app.App{first, second} {
		cfg := app.DefaultConfig()
		cfg.StateDir = t.TempDir()
		if errApply := instance.ApplyConfig(cfg); errApply != nil {
			t.Fatalf("apply config: %v", errApply)
		}
	}
	// 通过真实的 codearts 登录（纯本地构造）在第一个 App 上登记会话。
	rawStart, errStart := first.HandleMethod(pluginabi.MethodAuthLoginStart, mustMarshal(map[string]any{
		"metadata": map[string]any{"vendor": "codearts"},
	}))
	if errStart != nil {
		t.Fatalf("first app login start: %v", errStart)
	}
	payload := decodeEnvelopeResultForTest(t, rawStart)
	var started struct {
		State string `json:"state"`
	}
	if errUnmarshal := json.Unmarshal(payload, &started); errUnmarshal != nil {
		t.Fatalf("decode login start: %v", errUnmarshal)
	}
	if started.State == "" {
		t.Fatal("login session state is empty")
	}

	// 第二个 App 看不到第一个 App 的会话：按 state 轮询必须返回 session_not_found。
	raw, errPoll := second.HandleMethod(pluginabi.MethodAuthLoginPoll, mustMarshal(map[string]any{"state": started.State}))
	if errPoll == nil {
		t.Fatalf("second app should not own first app session, got %s", raw)
	}
}

// recordingVendor 是一个只记录 Configure 输入的供应商桩，用于验证
// 每个 App 的供应商实例各自收到自己的配置。
type recordingVendor struct {
	env     core.Env
	region  string
	baseURL string
	ua      string
	prompt  string
}

func (v *recordingVendor) ID() string                                { return "recording-" + v.region }
func (v *recordingVendor) Name() string                              { return "Recording " + v.region }
func (v *recordingVendor) Region() string                            { return v.region }
func (v *recordingVendor) ModelPrefix() string                       { return "" }
func (v *recordingVendor) Match(string, string, map[string]any) bool { return false }
func (v *recordingVendor) Parse([]byte, string) (*core.Credential, error) {
	return nil, nil
}
func (v *recordingVendor) StaticModels(context.Context) ([]pluginapi.ModelInfo, error) {
	return nil, nil
}
func (v *recordingVendor) Execute(context.Context, *core.Credential, *core.ExecuteRequest) (*pluginapi.ExecutorResponse, error) {
	return nil, nil
}
func (v *recordingVendor) ExecuteStream(context.Context, *core.Credential, *core.ExecuteRequest, core.StreamSink) error {
	return nil
}
func (v *recordingVendor) Configure(_ core.Settings, raw map[string]string) {
	v.baseURL = raw["vendor_base_url"]
	v.ua = raw["vendor_ua"]
	v.prompt = raw["prompt_mode"]
}

// TestAppsIsolateVendorConfig 验证两个 App 的供应商实例各自持有自己的配置。
func TestAppsIsolateVendorConfig(t *testing.T) {
	var firstVendor, secondVendor *recordingVendor
	first, errFirst := app.New(app.Options{Version: "a", Factories: []core.VendorFactory{{
		Instances: func(env core.Env) []core.Vendor {
			firstVendor = &recordingVendor{env: env, region: "cn"}
			return []core.Vendor{firstVendor}
		},
	}}})
	if errFirst != nil {
		t.Fatalf("new first: %v", errFirst)
	}
	second, errSecond := app.New(app.Options{Version: "b", Factories: []core.VendorFactory{{
		Instances: func(env core.Env) []core.Vendor {
			secondVendor = &recordingVendor{env: env, region: "cn"}
			return []core.Vendor{secondVendor}
		},
	}}})
	if errSecond != nil {
		t.Fatalf("new second: %v", errSecond)
	}

	firstCfg := app.DefaultConfig()
	firstCfg.StateDir = t.TempDir()
	firstCfg.Raw = map[string]string{
		"vendor_base_url": "http://first.test",
		"vendor_ua":       "UA-First/1.0",
	}
	secondCfg := app.DefaultConfig()
	secondCfg.StateDir = t.TempDir()
	secondCfg.Raw = map[string]string{
		"vendor_base_url": "http://second.test",
		"vendor_ua":       "UA-Second/2.0",
	}
	if errApply := first.ApplyConfig(firstCfg); errApply != nil {
		t.Fatalf("apply first: %v", errApply)
	}
	if errApply := second.ApplyConfig(secondCfg); errApply != nil {
		t.Fatalf("apply second: %v", errApply)
	}

	if firstVendor.baseURL != "http://first.test" || firstVendor.ua != "UA-First/1.0" {
		t.Fatalf("first vendor config = %+v", firstVendor)
	}
	if secondVendor.baseURL != "http://second.test" || secondVendor.ua != "UA-Second/2.0" {
		t.Fatalf("second vendor config = %+v", secondVendor)
	}
	if firstVendor == secondVendor {
		t.Fatal("apps share the same vendor instance")
	}
}
