package admin

import (
	"testing"

	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/state"
)

// newTagTestCatalog 构造一个带 zcode 供应商与已注册模型的目录，用于渠道标识测试。
func newTagTestCatalog(t *testing.T) *Catalog {
	t.Helper()
	store := state.NewStore()
	store.Configure(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	registry := core.NewRegistry()
	if err := registry.Register(&fakeVendor{Meta: core.Meta{VendorID: "zcode", VendorName: "ZCode", VendorRegion: "global"}}); err != nil {
		t.Fatal(err)
	}
	c := NewCatalog(store, registry)
	c.Configure(ModelSettings{})
	models := []pluginapi.ModelInfo{{ID: "GLM-5.3", Name: "GLM-5.3", DisplayName: "GLM-5.3"}}
	if !c.CacheProbedModels("zcode", models) {
		t.Fatal("register probe models")
	}
	return c
}

// TestChannelTagAlwaysPrefixes 验证对外 ID 恒定带「供应商-」前缀，
// 请求侧剥前缀还原官方 ID，未带前缀的原名同样可解析。
func TestChannelTagAlwaysPrefixes(t *testing.T) {
	c := newTagTestCatalog(t)
	out := c.ApplyAliases("zcode", []pluginapi.ModelInfo{{ID: "GLM-5.3", Name: "GLM-5.3", DisplayName: "GLM-5.3"}})
	if len(out) != 1 || out[0].ID != "zcode-GLM-5.3" {
		t.Fatalf("tagged id = %+v", out)
	}
	if got := c.ResolveOutboundModel("zcode", "zcode-GLM-5.3"); got != "GLM-5.3" {
		t.Fatalf("untag resolve = %q", got)
	}
	if got := c.ResolveOutboundModel("zcode", "GLM-5.3"); got != "GLM-5.3" {
		t.Fatalf("bare passthrough = %q", got)
	}
	// 其他供应商的请求不受影响（前缀只剥自己的 vendor 名）。
	if got := c.ResolveOutboundModel("workbuddycn", "zcode-GLM-5.3"); got != "zcode-GLM-5.3" {
		t.Fatalf("other vendor passthrough = %q", got)
	}
}

// TestChannelTagDoesNotShadowOtherModel 验证防呆：tag 后的名字撞上**同供应商
// 本次清单里的其他模型**时放弃标识，避免路由歧义。撞车判定用入参清单而非
// registeredLocked——后者对缓存为空的供应商会误判「已存在」导致标识被整体放弃。
func TestChannelTagDoesNotShadowOtherModel(t *testing.T) {
	c := newTagTestCatalog(t)
	out := c.ApplyAliases("zcode", []pluginapi.ModelInfo{
		{ID: "GLM-5.3", Name: "GLM-5.3"},
		{ID: "zcode-GLM-5.3", Name: "zcode-GLM-5.3"},
	})
	if len(out) != 2 || out[0].ID != "GLM-5.3" {
		t.Fatalf("collision must skip tagging, got %+v", out)
	}
}

// TestChannelTagResolveKeepsLegacyForms 验证还原不依赖历史模式：
// 客户端手里可能残留 zcode- 前缀或 -zcode 后缀两种形态，都要能还原。
func TestChannelTagResolveKeepsLegacyForms(t *testing.T) {
	c := newTagTestCatalog(t)
	if got := c.ResolveOutboundModel("zcode", "zcode-GLM-5.3"); got != "GLM-5.3" {
		t.Fatalf("prefix resolve = %q", got)
	}
	// 旧后缀形态同样可还原（历史配置留下的客户端模型名）。
	if got := c.ResolveOutboundModel("zcode", "GLM-5.3-zcode"); got != "GLM-5.3" {
		t.Fatalf("legacy suffix resolve = %q", got)
	}
}
