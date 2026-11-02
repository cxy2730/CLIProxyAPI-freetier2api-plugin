package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"freetier2api-plugin/cpasdk/pluginabi"
	"freetier2api-plugin/internal/core"
)

// 面板「OAuth 登录」列表按 oauth_provider 过滤，空则不渲染；
// 同时 credential_providers 必须认领 type=freetier，否则凭证全掉线。
func TestAuthIdentifierHidesOAuthButKeepsCredentialProvider(t *testing.T) {
	a := &App{}
	raw, err := a.HandleMethod(pluginabi.MethodAuthIdentifier, []byte(`{}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Identifier          string   `json:"identifier"`
			Identifiers         []string `json:"identifiers"`
			CredentialProviders []string `json:"credential_providers"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("bad json %s: %v", raw, err)
	}
	if !env.OK {
		t.Fatalf("not ok: %s", raw)
	}
	if env.Data.Identifier != "" || len(env.Data.Identifiers) != 0 {
		t.Fatalf("oauth identifiers must be empty: %s", raw)
	}
	if len(env.Data.CredentialProviders) != 1 || env.Data.CredentialProviders[0] != "freetier" {
		t.Fatalf("credential providers must claim freetier: %s", raw)
	}
	// executor / quota 的标识必须保持原样，否则执行器与额度都会找不到插件。
	for _, m := range []string{pluginabi.MethodExecutorIdentifier, pluginabi.MethodQuotaIdentifier} {
		out, _ := a.HandleMethod(m, []byte(`{}`))
		var e struct {
			Data struct {
				Identifier string `json:"identifier"`
			} `json:"result"`
		}
		json.Unmarshal(out, &e)
		if e.Data.Identifier != "freetier" {
			t.Fatalf("%s identifier changed: %s", m, out)
		}
	}
}

// 上游明示的等待时间必须出现在错误信封里（宿主会翻成 Retry-After 头）。
func TestFailureRetryAfterReachesEnvelope(t *testing.T) {
	f := &core.Failure{Kind: core.FailureKindSoftRate, Status: 429, Msg: "slow down", RetryWait: 1500 * time.Millisecond}
	var pe *core.PluginError
	if !errors.As(core.ToPluginError(f), &pe) {
		t.Fatal("not a PluginError")
	}
	if pe.RetryAfterSeconds != 2 {
		t.Fatalf("want 2s (ceil), got %d", pe.RetryAfterSeconds)
	}
	env := core.ErrorEnvelopeFromError(pe)
	if !strings.Contains(string(env), `"retry_after_seconds":2`) {
		t.Fatalf("envelope missing retry_after_seconds: %s", env)
	}
	// 没有等待信息时不得凭空发头。
	var plain *core.PluginError
	if !errors.As(core.ToPluginError(&core.Failure{Kind: core.FailureKindServer, Status: 500, Msg: "boom"}), &plain) {
		t.Fatal("not a PluginError")
	}
	if plain.RetryAfterSeconds != 0 {
		t.Fatalf("unexpected retry hint: %d", plain.RetryAfterSeconds)
	}
}
