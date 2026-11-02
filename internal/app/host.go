package app

// 本文件是 App 的宿主面：core.Host 实现、供应商配置下发、后台/流生命周期，
// 以及管理接口依赖注入。

import (
	"context"
	"sync"
	"time"

	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/admin"
	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/hostbridge"
	"freetier2api-plugin/internal/logger"
	"freetier2api-plugin/internal/state"
)

type hostBridge struct{ app *App }

func (h *hostBridge) ListAuths(ctx context.Context) ([]core.HostAuth, error) {
	return hostbridge.ListAuths(ctx, "", h.app.registry)
}

func (h *hostBridge) AuthJSON(ctx context.Context, authIndex string) ([]byte, bool) {
	return hostbridge.AuthJSON(ctx, "", authIndex)
}

func (h *hostBridge) Settings() core.Settings {
	cfg := h.app.Config()
	return core.Settings{
		EnabledRealms: append([]string(nil), cfg.EnabledRealms...),
		StateDir:      cfg.StateDir,
		MachineSalt:   h.app.machineSalt,
		AutoCheckin:   cfg.AutoCheckin,
		AutoCheckinAt: cfg.AutoCheckinAt,
		AutoTasks:     cfg.AutoTasks,
		LogLevel:      cfg.LogLevel,
	}
}

// configureVendors 把全局设置与原始配置键推给每个供应商。
func (a *App) configureVendors(cfg pluginConfig) {
	settings := a.host.Settings()
	raw := make(map[string]string, len(cfg.Raw)+1)
	for key, value := range cfg.Raw {
		raw[key] = value
	}
	// 提示词过滤是全局设置：页面覆盖 > YAML > 内置默认，供应商读同一个键。
	for _, vendor := range a.registry.Vendors() {
		if configurable, okConfigurable := vendor.(core.Configurable); okConfigurable {
			configurable.Configure(settings, raw)
		}
	}
}

const DefaultShutdownWaitTimeout = 10 * time.Second

// StartBackgroundWork 启动后台循环（幂等）。
func (a *App) StartBackgroundWork() {
	a.backgroundMu.Lock()
	defer a.backgroundMu.Unlock()
	if a.backgroundCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.backgroundCancel = cancel
	a.tasks.Start(ctx)
}

// StopBackgroundWork 取消后台循环并等待退出。
func (a *App) StopBackgroundWork() {
	a.backgroundMu.Lock()
	cancel := a.backgroundCancel
	a.backgroundCancel = nil
	a.backgroundMu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.tasks.Stop()
	a.backgroundWG.Wait()
	// 写盘是合并到短窗口里做的（state.saveDelay）：关停前必须 Flush——它既把
	// 窗口内的改动写掉，也取消挂起的定时器（否则卸载后定时器仍会醒来碰状态）。
	a.store.Flush()
}

// BackgroundRunning 报告后台循环是否运行。
func (a *App) BackgroundRunning() bool {
	a.backgroundMu.Lock()
	defer a.backgroundMu.Unlock()
	return a.backgroundCancel != nil
}

// BeginPluginStream 注册一个流式转发任务。
func (a *App) BeginPluginStream() (context.Context, context.CancelFunc, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	a.streamMu.Lock()
	a.nextStreamKey++
	key := a.nextStreamKey
	a.streamCancels[key] = cancel
	a.streamMu.Unlock()
	a.streamWG.Add(1)
	var once sync.Once
	finish := func() {
		once.Do(func() {
			a.streamMu.Lock()
			delete(a.streamCancels, key)
			a.streamMu.Unlock()
			cancel()
			a.streamWG.Done()
		})
	}
	return ctx, cancel, finish
}

// WaitPluginStreams 取消并等待在途流。
func (a *App) WaitPluginStreams(timeout time.Duration) {
	a.streamMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(a.streamCancels))
	for _, cancel := range a.streamCancels {
		cancels = append(cancels, cancel)
	}
	inflight := len(a.streamCancels)
	a.streamMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	if inflight == 0 {
		a.streamWG.Wait()
		return
	}
	if timeout <= 0 {
		timeout = DefaultShutdownWaitTimeout
	}
	done := make(chan struct{})
	go func() {
		a.streamWG.Wait()
		close(done)
	}()
	start := time.Now()
	select {
	case <-done:
	case <-time.After(timeout):
		a.streamMu.Lock()
		remaining := len(a.streamCancels)
		a.streamMu.Unlock()
		logger.Error("关闭：等待 %s 后仍有 %d 个流任务在运行", time.Since(start).Round(time.Millisecond), remaining)
	}
}

// adminDeps 实现 admin.Deps。
type adminDeps struct{ app *App }

func (d adminDeps) Identity() admin.Identity {
	return admin.Identity{ID: pluginID, DisplayName: pluginDisplayName, ProviderKey: providerKey, Version: d.app.version}
}

func (d adminDeps) Config() admin.PluginConfig { return d.app.Config() }
func (d adminDeps) State() state.State         { return d.app.SnapshotState() }
func (d adminDeps) MutateState(fn func(*state.State)) {
	d.app.MutateState(fn)
}
func (d adminDeps) Model() *admin.Catalog { return d.app.models }

func (d adminDeps) RefreshModels(ctx context.Context) (int, error) {
	return d.app.refreshModelsFromUpstream(ctx)
}

func (d adminDeps) Reconfigure() error { return d.app.ApplyConfig(d.app.Config()) }

// SubmitLoginCallback 把页面粘回来的 localhost 回调链接交给对应供应商处理。
func (d adminDeps) SendLoginSMS(ctx context.Context, vendorID, phone string) (string, error) {
	return d.app.SendLoginSMS(ctx, vendorID, phone)
}

func (d adminDeps) VerifyLoginSMS(ctx context.Context, vendorID, sessionKey, phone, code string) (string, string, string, error) {
	return d.app.VerifyLoginSMS(ctx, vendorID, sessionKey, phone, code)
}

func (d adminDeps) SubmitLoginCallback(ctx context.Context, state, rawURL string) (*core.CallbackSubmitResult, error) {
	return d.app.SubmitLoginCallback(ctx, state, rawURL)
}

func (d adminDeps) StartLogin(ctx context.Context, vendorID string, metadata map[string]any) (*pluginapi.AuthLoginStartResponse, error) {
	return d.app.StartLogin(ctx, vendorID, metadata)
}

func (d adminDeps) LoginCaptchaConfig(ctx context.Context, vendorID string) (core.CaptchaConfig, error) {
	return d.app.LoginCaptchaConfig(ctx, vendorID)
}

func (a *App) managementRegistration() pluginapi.ManagementRegistrationResponse {
	return a.admin.Registration()
}

func (a *App) handleManagement(request []byte) ([]byte, error) { return a.admin.Handle(request) }
