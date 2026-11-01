package app

import (
	"testing"

	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/state"
)

// 额度读数的保留与失效。
//
// 上游限流/超时/5xx 是常态：这类失败时把额度列清空，用户分不清「没额度了」
// 和「这次没取到」。保留上次读数并显式标 Stale，比给一个空白更有信息量。

func newSnapshotApp() *App {
	return &App{store: state.NewStore()}
}

func snapshotResponse(label string) *pluginapi.QuotaFetchResponse {
	return &pluginapi.QuotaFetchResponse{
		Summary:   []pluginapi.QuotaMetric{{Key: "credit", Label: label, Value: 2000, Unit: "credit"}},
		FetchedAt: "2026-10-21T00:00:00Z",
	}
}

// TestQuotaSnapshotRoundTrip 验证：存进去的读数能原样读回来。
func TestQuotaSnapshotRoundTrip(t *testing.T) {
	app := newSnapshotApp()
	app.storeQuotaSnapshot("qodercn", "auth-1", snapshotResponse("剩余 2000/3000"))

	cached, ok := app.cachedQuotaSnapshot("qodercn", "auth-1")
	if !ok {
		t.Fatal("应能读回刚存的读数")
	}
	if len(cached.Summary) != 1 || cached.Summary[0].Label != "剩余 2000/3000" {
		t.Fatalf("summary 未原样保留: %+v", cached.Summary)
	}
	if cached.FetchedAt != "2026-10-21T00:00:00Z" {
		t.Fatalf("fetched_at = %q, 期望保留原时间", cached.FetchedAt)
	}
	// 存进去的快照本身必须是「干净读数」：Stale 是本次请求的临时状态。
	if cached.Stale || cached.StaleReason != "" {
		t.Fatalf("快照不应带 Stale 状态，实际 stale=%v reason=%q", cached.Stale, cached.StaleReason)
	}
}

// TestQuotaSnapshotDoesNotPersistStale 验证：带 Stale 的响应存进去也会被清洗。
//
// 若把 Stale 一起落盘，下一次读出来会直接显示成缓存值，用户永远看不到实时数据。
func TestQuotaSnapshotDoesNotPersistStale(t *testing.T) {
	app := newSnapshotApp()
	response := snapshotResponse("旧读数")
	response.Stale = true
	response.StaleReason = "上一次失败"
	app.storeQuotaSnapshot("qodercn", "auth-1", response)

	cached, ok := app.cachedQuotaSnapshot("qodercn", "auth-1")
	if !ok {
		t.Fatal("应能读回")
	}
	if cached.Stale || cached.StaleReason != "" {
		t.Fatalf("落盘时未清洗 Stale，读回 stale=%v reason=%q", cached.Stale, cached.StaleReason)
	}
}

// TestQuotaSnapshotSeparatesVendors 验证：同一 authID 在不同供应商下互不覆盖。
func TestQuotaSnapshotSeparatesVendors(t *testing.T) {
	app := newSnapshotApp()
	app.storeQuotaSnapshot("qodercn", "user", snapshotResponse("qoder 的读数"))
	app.storeQuotaSnapshot("traecn", "user", snapshotResponse("trae 的读数"))

	if cached, _ := app.cachedQuotaSnapshot("qodercn", "user"); cached.Summary[0].Label != "qoder 的读数" {
		t.Fatalf("qoder 读数被覆盖：%q", cached.Summary[0].Label)
	}
	if cached, _ := app.cachedQuotaSnapshot("traecn", "user"); cached.Summary[0].Label != "trae 的读数" {
		t.Fatalf("trae 读数被覆盖：%q", cached.Summary[0].Label)
	}
}

// TestQuotaSnapshotMisses 验证：没有缓存时明确返回 false，调用方据此报错。
func TestQuotaSnapshotMisses(t *testing.T) {
	app := newSnapshotApp()
	if _, ok := app.cachedQuotaSnapshot("qodercn", "never-fetched"); ok {
		t.Fatal("没有缓存时不应返回读数")
	}
	// 空 vendor 或空 authID 一律不缓存，避免写出 "|auth" 这种无意义键。
	if key := quotaSnapshotKey("", "auth-1"); key != "" {
		t.Fatalf("空供应商不应产生键，实际 %q", key)
	}
	if key := quotaSnapshotKey("qodercn", ""); key != "" {
		t.Fatalf("空凭证不应产生键，实际 %q", key)
	}
}

// TestQuotaSnapshotSurvivesReload 验证：快照随状态文件落盘，重开后还在。
func TestQuotaSnapshotSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStore()
	store.Configure(dir)
	if _, errLoad := store.Load(); errLoad != nil {
		t.Fatalf("加载状态: %v", errLoad)
	}
	app := &App{store: store}
	app.storeQuotaSnapshot("qodercn", "auth-1", snapshotResponse("落盘读数"))
	store.Flush()

	reopened := state.NewStore()
	reopened.Configure(dir)
	if _, errLoad := reopened.Load(); errLoad != nil {
		t.Fatalf("重开状态: %v", errLoad)
	}
	cached, ok := (&App{store: reopened}).cachedQuotaSnapshot("qodercn", "auth-1")
	if !ok {
		t.Fatal("重开后应能读回快照")
	}
	if cached.Summary[0].Label != "落盘读数" {
		t.Fatalf("重开后 label = %q", cached.Summary[0].Label)
	}
}
