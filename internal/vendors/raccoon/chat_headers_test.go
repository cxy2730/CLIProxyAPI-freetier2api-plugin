package raccoon

import (
	"net/http"
	"strings"
	"testing"
)

// 客户端识别头的行为测试。
//
// 上游按客户端渠道做风控，所以「版本号与平台头是否自洽」比头本身更关键：
// 声明 desktop-windows-x64 却报 Linux 构建的版本，等于自报不是官方客户端。

// TestApplyChatHeadersAdvertisesWindowsDesktop 锁住对话域的四个识别头。
func TestApplyChatHeadersAdvertisesWindowsDesktop(t *testing.T) {
	req, errNew := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if errNew != nil {
		t.Fatalf("new request: %v", errNew)
	}
	ApplyChatHeaders(req, "tok")

	want := map[string]string{
		"x-client-name":     "@raccoon/office-desktop",
		"x-client-platform": "desktop-windows-x64",
		"x-client-version":  DefaultClientVersion,
		"x-client-channel":  "official",
	}
	for header, expected := range want {
		if got := req.Header.Get(header); got != expected {
			t.Errorf("%s = %q, 期望 %q", header, got, expected)
		}
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
}

// TestGrantHeadersUseGrantClientLine 锁住「登录奖励域读的是自己的版本常量」。
//
// 两个域的平台头不同（desktop-windows vs desktop-windows-x64，前者是上游要求
// 的固定枚举），版本也**刻意各声明一份**——见 chat.go 里 grantClientVersion 的
// 说明：两个值都没有抓包支撑，复用会把「它们本来就该一致」变成循环论证。
//
// 所以这里只断言登录域发出的是 grantClientVersion 的值，**不锁两域同值**：
// 日后任一域抓到真实流量，改那一个即可，另一个不受牵连。
//
// 边界（如实说明）：两个常量当前取值相同，因此本条断言**无法**区分实现读的
// 是哪一个常量——值对就通过。来源约束靠代码本身（ApplyGrantHeaders 直接引用
// grantClientVersion），这里不假装能验证它。日后两值一旦分叉，这条断言立刻
// 具备区分能力。
func TestGrantHeadersUseGrantClientLine(t *testing.T) {
	req, errNew := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if errNew != nil {
		t.Fatalf("new request: %v", errNew)
	}
	ApplyGrantHeaders(req, "tok", "", "dev-1")

	if got := req.Header.Get("X-Client-Platform"); got != grantClientPlatform {
		t.Errorf("X-Client-Platform = %q, 期望 %q", got, grantClientPlatform)
	}
	if got := req.Header.Get("X-Client-Version"); got != grantClientVersion {
		t.Errorf("X-Client-Version = %q, 期望 %q（登录域读 grantClientVersion）",
			got, grantClientVersion)
	}
	// 该头必须存在，即使值为空串。
	if _, ok := req.Header["X-Org-Code"]; !ok {
		t.Error("X-Org-Code 头缺失（上游总是发送，空值也要有）")
	}
}

// TestGrantClientVersionIsVersioned 锁住登录域的版本形态：与 DefaultClientVersion
// 同样必须带 v 前缀。两份常量各自受形态约束，谁改了都不会溜过去。
func TestGrantClientVersionIsVersioned(t *testing.T) {
	if !strings.HasPrefix(grantClientVersion, "v") {
		t.Fatalf("grantClientVersion = %q, 期望带 v 前缀", grantClientVersion)
	}
	if len(strings.TrimPrefix(grantClientVersion, "v")) == 0 {
		t.Fatalf("grantClientVersion = %q 缺版本号", grantClientVersion)
	}
}

// TestDefaultClientVersionIsVersioned 锁住版本号形态：上游 x-client-version
// 带 v 前缀（v1.1.3），写成 1.1.3 会被当成非官方构建。
func TestDefaultClientVersionIsVersioned(t *testing.T) {
	if !strings.HasPrefix(DefaultClientVersion, "v") {
		t.Fatalf("DefaultClientVersion = %q, 期望带 v 前缀", DefaultClientVersion)
	}
	if len(strings.TrimPrefix(DefaultClientVersion, "v")) == 0 {
		t.Fatalf("DefaultClientVersion = %q 缺版本号", DefaultClientVersion)
	}
}
