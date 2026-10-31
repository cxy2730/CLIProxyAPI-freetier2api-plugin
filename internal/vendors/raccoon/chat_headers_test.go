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

// TestGrantHeadersMatchChatClientLine 锁住「登录奖励域与对话域同一发布版本」。
//
// 两个域的平台头不同（desktop-windows vs desktop-windows-x64，前者是上游
// 要求的固定枚举），但版本必须来自同一处，否则一次升级只改一边就会出现
// 「同一个客户端在两个端点报两个版本」。
func TestGrantHeadersMatchChatClientLine(t *testing.T) {
	req, errNew := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if errNew != nil {
		t.Fatalf("new request: %v", errNew)
	}
	ApplyGrantHeaders(req, "tok", "", "dev-1")

	if got := req.Header.Get("X-Client-Platform"); got != grantClientPlatform {
		t.Errorf("X-Client-Platform = %q, 期望 %q", got, grantClientPlatform)
	}
	if got := req.Header.Get("X-Client-Version"); got != DefaultClientVersion {
		t.Errorf("X-Client-Version = %q, 期望 %q（两域必须同源）", got, DefaultClientVersion)
	}
	if grantClientVersion != DefaultClientVersion {
		t.Errorf("grantClientVersion = %q 已与 DefaultClientVersion 分叉，确认是否有意为之",
			grantClientVersion)
	}
	// 该头必须存在，即使值为空串。
	if _, ok := req.Header["X-Org-Code"]; !ok {
		t.Error("X-Org-Code 头缺失（上游总是发送，空值也要有）")
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
