package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/auth"
	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/state"
	"freetier2api-plugin/internal/tasks"
)

type stubDeps struct {
	store    *state.Store
	registry *core.Registry
	manager  *Catalog

	callbackResult *core.CallbackSubmitResult
	callbackErr    error

	loginStart    *pluginapi.AuthLoginStartResponse
	loginStartErr error
	captcha       core.CaptchaConfig
	captchaErr    error
}

func (s stubDeps) Identity() Identity {
	return Identity{ID: "freetier", DisplayName: "FreeTier 2API", ProviderKey: "freetier", Version: "test"}
}
func (s stubDeps) Config() PluginConfig { return DefaultPluginConfig() }
func (s stubDeps) State() state.State   { return s.store.Snapshot() }
func (s stubDeps) MutateState(fn func(*state.State)) {
	s.store.Mutate(fn)
}
func (s stubDeps) Model() *Catalog { return s.manager }
func (s stubDeps) ResolveCredential(context.Context, string, []byte, string, map[string]string) (*core.Credential, core.Vendor, error) {
	return nil, nil, context.Canceled
}
func (s stubDeps) RefreshModels(context.Context) (int, error) { return 0, nil }
func (s stubDeps) Reconfigure() error                         { return nil }

// callbackResult / callbackErr 让用例能驱动「粘贴回调链接」的返回分支。
func (s stubDeps) SendLoginSMS(context.Context, string, string) (string, error) {
	return "", nil
}

func (s stubDeps) VerifyLoginSMS(context.Context, string, string, string, string) (string, string, string, error) {
	return "", "", "", nil
}

func (s stubDeps) SubmitLoginCallback(context.Context, string, string) (*core.CallbackSubmitResult, error) {
	if s.callbackErr != nil {
		return nil, s.callbackErr
	}
	return s.callbackResult, nil
}

// loginStart / loginCaptcha 让用例能驱动页面内登录的两个新入口。
func (s stubDeps) StartLogin(context.Context, string, map[string]any) (*pluginapi.AuthLoginStartResponse, error) {
	if s.loginStartErr != nil {
		return nil, s.loginStartErr
	}
	return s.loginStart, nil
}

func (s stubDeps) LoginCaptchaConfig(context.Context, string) (core.CaptchaConfig, error) {
	if s.captchaErr != nil {
		return core.CaptchaConfig{}, s.captchaErr
	}
	return s.captcha, nil
}

func newStubDeps(t *testing.T) stubDeps {
	t.Helper()
	store := state.NewStore()
	store.Configure(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	registry := core.NewRegistry()
	return stubDeps{store: store, registry: registry, manager: NewCatalog(store, registry)}
}

func newService(t *testing.T) *Service {
	t.Helper()
	return newServiceWithDeps(t, newStubDeps(t))
}

func newServiceWithDeps(t *testing.T, deps stubDeps) *Service {
	t.Helper()
	pool := auth.NewPool(deps.registry, deps.ResolveCredential)
	return New(deps, deps.registry, pool, tasks.New(stubTaskDeps{deps: deps, pool: pool}))
}

func TestRegistrationDeclaresRoutes(t *testing.T) {
	service := newService(t)
	registration := service.Registration()
	found := false
	for _, route := range registration.Routes {
		if route.Path == "/plugins/freetier/status" {
			found = true
		}
	}
	if !found {
		t.Fatalf("status route missing: %#v", registration.Routes)
	}
	// 控制台保留（账号、额度、模型、设置），任务中心与日志页已下线。
	var console *pluginapi.ResourceRoute
	for i := range registration.Resources {
		if registration.Resources[i].Path == "/console" {
			console = &registration.Resources[i]
		}
	}
	if console == nil {
		t.Fatalf("console resource missing: %#v", registration.Resources)
	}
	if console.Menu == "" || console.Description == "" {
		t.Fatalf("console resource must declare menu/description: %#v", *console)
	}
}

func TestHandleStatusAndUnknownRoute(t *testing.T) {
	service := newService(t)

	raw, err := service.Handle([]byte(`{"method":"GET","path":"/v0/management/plugins/freetier/status"}`))
	if err != nil {
		t.Fatalf("handle status: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !strings.Contains(string(raw), `"StatusCode":200`) && !strings.Contains(string(raw), `"status_code":200`) {
		t.Fatalf("unexpected envelope: %s", raw)
	}

	raw, err = service.Handle([]byte(`{"method":"GET","path":"/plugins/freetier/nope"}`))
	if err != nil {
		t.Fatalf("handle unknown: %v", err)
	}
	var notFound struct {
		Result struct {
			StatusCode int
		}
	}
	if err := json.Unmarshal(raw, &notFound); err != nil {
		t.Fatalf("decode not-found: %v", err)
	}
	if notFound.Result.StatusCode != 404 {
		t.Fatalf("unknown route should 404: %s", raw)
	}
}

func TestHandleServesConsolePage(t *testing.T) {
	service := newService(t)
	raw, err := service.Handle([]byte(`{"method":"GET","path":"/v0/resource/plugins/freetier/console"}`))
	if err != nil {
		t.Fatalf("handle console: %v", err)
	}
	var envelope struct {
		Result struct {
			StatusCode int                 `json:"StatusCode"`
			Headers    map[string][]string `json:"Headers"`
			Body       []byte              `json:"Body"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, raw)
	}
	if envelope.Result.StatusCode != http.StatusOK {
		t.Fatalf("console status = %d, want 200", envelope.Result.StatusCode)
	}
	page := string(envelope.Result.Body)
	for _, want := range []string{"FreeTier 2API", "view-accounts", "view-settings"} {
		if !strings.Contains(page, want) {
			t.Fatalf("console page missing %q", want)
		}
	}
	for _, gone := range []string{"view-tasks", "view-logs", "/logs"} {
		if strings.Contains(page, gone) {
			t.Fatalf("console page still references %q", gone)
		}
	}
}

type stubTaskDeps struct {
	deps stubDeps
	pool *auth.Pool
}

func (s stubTaskDeps) Registry() *core.Registry { return s.deps.registry }
func (s stubTaskDeps) Pool() *auth.Pool         { return s.pool }

// Config 把全局配置投影成任务引擎的最小形状（与组合根 taskConfig 同构）。
func (s stubTaskDeps) Config() tasks.Config {
	cfg := s.deps.Config()
	return tasks.Config{
		EnabledRealms: cfg.EnabledRealms, AutoCheckin: cfg.AutoCheckin,
		AutoCheckinAt: cfg.AutoCheckinAt, AutoTasks: cfg.AutoTasks,
	}
}

func (s stubTaskDeps) SaveRefreshedCredential(context.Context, string, core.Vendor, core.HostAuth, []byte, *core.Credential, string) {
}
func (s stubTaskDeps) State() state.State                { return s.deps.State() }
func (s stubTaskDeps) MutateState(fn func(*state.State)) { s.deps.MutateState(fn) }
func (s stubTaskDeps) Now() time.Time                    { return time.Now() }

// 一键任务的两个端点必须真的挂上：控制台按钮只认这两个路径，
// 少一条就是「点了没反应」。
func TestHandleAutoAllRoutes(t *testing.T) {
	service := newService(t)

	type envelope struct {
		Result struct {
			StatusCode int    `json:"StatusCode"`
			Body       []byte `json:"Body"`
		} `json:"result"`
	}
	decode := func(raw []byte) envelope {
		var out envelope
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode envelope: %v (%s)", err, raw)
		}
		return out
	}

	raw, err := service.Handle([]byte(`{"method":"GET","path":"/plugins/freetier/tasks/autoall/status"}`))
	if err != nil {
		t.Fatalf("handle autoall status: %v", err)
	}
	status := decode(raw)
	if status.Result.StatusCode != http.StatusOK {
		t.Fatalf("autoall status = %d, want 200", status.Result.StatusCode)
	}
	if !strings.Contains(string(status.Result.Body), `"running"`) {
		t.Fatalf("status payload must expose running: %s", status.Result.Body)
	}

	// 坏 JSON 走解析失败分支（400），证明 POST 路由已挂上；404 就是没挂。
	raw, err = service.Handle([]byte(`{"method":"POST","path":"/plugins/freetier/tasks/autoall","body":"ew=="}`))
	if err != nil {
		t.Fatalf("handle autoall start: %v", err)
	}
	start := decode(raw)
	if start.Result.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body should 400 (route must be wired), got %d", start.Result.StatusCode)
	}
}
