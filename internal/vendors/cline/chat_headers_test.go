package cline

import (
	"net/http"
	"testing"
)

// 客户端伪装头的行为测试。
//
// 这些头不参与业务，却直接决定上游怎么给这波流量归类：版本号与
// X-CLIENT-TYPE 自相矛盾（比如声明 cline-cli 却报扩展的 4.x 版本）会让请求
// 比不带头更可疑。所以版本线必须钉死在 CLI 发布线上。

// TestApplyChatHeadersAdvertisesCLIIdentity 锁住「声明的身份是 CLI」。
func TestApplyChatHeadersAdvertisesCLIIdentity(t *testing.T) {
	req, errNew := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if errNew != nil {
		t.Fatalf("new request: %v", errNew)
	}
	ApplyChatHeaders(req, "tok", "task-1")

	if got := req.Header.Get("X-CLIENT-TYPE"); got != "cline-cli" {
		t.Errorf("X-CLIENT-TYPE = %q, 期望 cline-cli", got)
	}
	if got := req.Header.Get("X-PLATFORM"); got != "terminal" {
		t.Errorf("X-PLATFORM = %q, 期望 terminal", got)
	}
	// 三个版本头必须与 UA 同源，否则一个请求里出现两个客户端版本。
	for _, header := range []string{"X-CLIENT-VERSION", "X-PLATFORM-VERSION"} {
		if got := req.Header.Get(header); got != DefaultClientVersion {
			t.Errorf("%s = %q, 期望 %q", header, got, DefaultClientVersion)
		}
	}
	if want := "Cline/" + DefaultClientVersion; req.Header.Get("User-Agent") != want {
		t.Errorf("User-Agent = %q, 期望 %q", req.Header.Get("User-Agent"), want)
	}
}

// TestDefaultClientVersionFollowsCLILine 锁住「版本取 CLI 线，不取扩展线」。
//
// github.com/cline/cline 的 releases 页混着 VS Code 扩展与 CLI 两条线：4.x
// 是扩展的版本号，CLI 仍是 3.x。把扩展版本填进 cline-cli 的身份头属于自相
// 矛盾。这里用主版本号做守门，防止有人下次又「顺手升到最新」。
func TestDefaultClientVersionFollowsCLILine(t *testing.T) {
	if DefaultClientVersion == "" {
		t.Fatal("DefaultClientVersion 不能为空")
	}
	if DefaultClientVersion[0] != '3' {
		t.Fatalf("DefaultClientVersion = %q, 期望 3.x（CLI 发布线；4.x 是 VS Code 扩展的版本）", DefaultClientVersion)
	}
	if DefaultCoreVersion == "" {
		t.Error("DefaultCoreVersion 不能为空")
	}
}

// TestApplyJSONHeadersOmitsClientSpoof 锁住「登录/续期链路不带伪装头」。
func TestApplyJSONHeadersOmitsClientSpoof(t *testing.T) {
	req, errNew := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if errNew != nil {
		t.Fatalf("new request: %v", errNew)
	}
	ApplyJSONHeaders(req)
	if got := req.Header.Get("X-CLIENT-TYPE"); got != "" {
		t.Errorf("登录链路不该带 X-CLIENT-TYPE，得到 %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "" {
		t.Errorf("登录链路不该带伪造 UA，得到 %q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
}
