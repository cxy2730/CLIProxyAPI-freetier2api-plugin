package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"freetier2api-plugin/cpasdk/pluginabi"
	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/app"
	"freetier2api-plugin/internal/hostbridge"
	"freetier2api-plugin/internal/vendors/codearts"
)

// 通过插件 ABI 覆盖配置 → 账号解析 → 流式/非流式执行 → 宿主出站头。
// 上游使用模拟响应，不消耗任何真实账号积分。
func TestCodeArtsCreditChannelRequiresExplicitOptIn(t *testing.T) {
	for _, setting := range []string{"", "false", "invalid", "true", "disable-after-enable"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("setting=%q/stream=%v", setting, stream), func(t *testing.T) {
				host := installFakeHost(t)
				cfg := setupTestPlugin(t, func(cfg *app.Config) {
					value := setting
					if setting == "disable-after-enable" {
						value = "true"
					}
					cfg.Raw = map[string]string{"codearts_credit_enabled": value}
				})
				if setting == "disable-after-enable" {
					cfg.Raw = nil
					if err := testApp.ApplyConfig(cfg); err != nil {
						t.Fatal(err)
					}
				}
				host.addAccount("1", "codearts-user-1.json", codeartsCredentialJSON)
				requests := make(chan map[string][]string, 2)
				hostbridge.Configure(func(id, method string, payload any) (json.RawMessage, error) {
					if method == pluginabi.MethodHostHTTPDo {
						raw, _ := json.Marshal(payload)
						var req struct {
							URL     string              `json:"url"`
							Headers map[string][]string `json:"headers"`
						}
						_ = json.Unmarshal(raw, &req)
						if strings.HasSuffix(req.URL, codearts.EpChatV2) {
							requests <- req.Headers
						}
					}
					return host.call(id, method, payload)
				})
				host.setUpstream(func(method, url, body string) fakeUpstreamResponse {
					if !strings.HasSuffix(url, codearts.EpChatV2) {
						return fakeUpstreamResponse{Status: http.StatusNotFound, Body: `{}`}
					}
					text := `{"id":"fixture","choices":[{"message":{"role":"assistant","content":"PONG"},"finish_reason":"stop"}]}`
					if stream {
						text = "data: {\"choices\":[{\"delta\":{\"content\":\"PONG\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
					}
					return fakeUpstreamResponse{Status: http.StatusOK, Body: text}
				})
				req := app.ExecutorRPCRequest{ExecutorRequest: pluginapi.ExecutorRequest{
					AuthID: "1", Model: "deepseek-v4.1-flash-1017", Format: app.FormatChatCompletions,
					Payload: []byte(fmt.Sprintf(`{"model":"deepseek-v4.1-flash-1017","messages":[{"role":"user","content":"PONG"}],"max_tokens":64,"stream":%v}`, stream)),
				}}
				if stream {
					req.StreamID = "credit-fixture"
					callMethod(t, pluginabi.MethodExecutorExecuteStream, req)
					if !waitFor(t, 3e9, func() bool { return len(host.emittedChunks()) > 0 }) {
						t.Fatal("no stream output")
					}
					if !strings.Contains(host.emittedChunks()[0], "PONG") {
						t.Fatalf("unexpected stream: %v", host.emittedChunks())
					}
				} else {
					raw := callMethod(t, pluginabi.MethodExecutorExecute, req)
					var response pluginapi.ExecutorResponse
					if err := json.Unmarshal(raw, &response); err != nil || !strings.Contains(string(response.Payload), "PONG") {
						t.Fatalf("unexpected response: %s (%v)", raw, err)
					}
				}
				if len(requests) != 1 {
					t.Fatalf("expected exactly one inference, got %d", len(requests))
				}
				headers := http.Header(<-requests)
				wantBenefit := setting != "true"
				if (headers.Get("maas_type") == "benefit") != wantBenefit {
					t.Fatalf("wrong billing channel for %q: %v", setting, headers.Get("maas_type"))
				}
				if strings.Contains(headers.Get("Authorization"), "maas_type") != wantBenefit {
					t.Fatal("channel header/signature mismatch")
				}
				// 模型头属于积分通道：福利通道是既有默认路径，不加没有实盘证据的头。
				for _, key := range []string{"Model-Id", "Model-Name"} {
					want := req.Model
					if wantBenefit {
						want = ""
					}
					if headers.Get(key) != want {
						t.Fatalf("%s = %q, want %q (benefit=%v)", key, headers.Get(key), want, wantBenefit)
					}
				}
			})
		}
	}
}

func TestCodeArtsCreditCatalogUsesOnlyBuiltinModels(t *testing.T) {
	host := installFakeHost(t)
	setupTestPlugin(t, func(cfg *app.Config) { cfg.Raw = map[string]string{"codearts_credit_enabled": "true"} })
	host.addAccount("1", "codearts-user-1.json", codeartsCredentialJSON)
	var benefitCalls atomic.Int32
	host.setUpstream(func(method, url, body string) fakeUpstreamResponse {
		switch {
		case strings.HasSuffix(url, codearts.EpModelBuiltin):
			return fakeUpstreamResponse{Status: http.StatusOK, Body: `{"builtinModels":[{"model_id":"deepseek-v4.1-flash-1017","enable":true}]}`}
		case strings.HasSuffix(url, codearts.EpBenefitConfig):
			benefitCalls.Add(1)
			return fakeUpstreamResponse{Status: http.StatusOK, Body: `{"error_code":"0000","result":{"models":[{"model_id":"benefit-only"}]}}`}
		}
		return fakeUpstreamResponse{Status: http.StatusNotFound, Body: `{}`}
	})
	for _, item := range []struct {
		method  string
		request any
	}{
		{pluginabi.MethodModelStatic, pluginapi.StaticModelRequest{}},
		{pluginabi.MethodModelForAuth, pluginapi.AuthModelRequest{AuthID: "1"}},
	} {
		var response pluginapi.ModelResponse
		raw := callMethod(t, item.method, item.request)
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		ids := modelIDSet(t, response.Models)
		if len(ids) != 1 || !ids["deepseek-v4.1-flash-1017"] {
			t.Fatalf("credit catalog must not contain guessed or benefit-only models: %v", ids)
		}
	}
	if benefitCalls.Load() != 0 {
		t.Fatal("credit mode must not query the benefit catalog")
	}
	// 上游目录失败时不得改用硬编码清单继续推广可计费模型。
	host.setUpstream(func(method, url, body string) fakeUpstreamResponse {
		return fakeUpstreamResponse{Status: http.StatusServiceUnavailable, Body: `{}`}
	})
	raw := callMethod(t, pluginabi.MethodModelForAuth, pluginapi.AuthModelRequest{AuthID: "1"})
	var response pluginapi.ModelResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Models) != 0 {
		t.Fatalf("failed credit catalog discovery must not guess models: %v", response.Models)
	}
}

func TestCodeArtsBenefitFailureNeverRetriesOnCredit(t *testing.T) {
	host := installFakeHost(t)
	setupTestPlugin(t)
	host.addAccount("1", "codearts-user-1.json", codeartsCredentialJSON)
	var calls atomic.Int32
	host.setUpstream(func(method, url, body string) fakeUpstreamResponse {
		if strings.HasSuffix(url, codearts.EpChatV2) {
			calls.Add(1)
			return fakeUpstreamResponse{Status: http.StatusOK, Body: `{"error_code":"4004","error_msg":"benefit not found"}`}
		}
		return fakeUpstreamResponse{Status: http.StatusNotFound, Body: `{}`}
	})
	_, err := testApp.HandleMethod(pluginabi.MethodExecutorExecute, mustMarshal(app.ExecutorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{AuthID: "1", Model: "deepseek-v4.1-flash-1017", Format: app.FormatChatCompletions,
			Payload: []byte(`{"model":"deepseek-v4.1-flash-1017","messages":[],"max_tokens":64}`)},
	}))
	if err == nil || calls.Load() != 1 {
		t.Fatalf("benefit error must be returned without a credit retry: %v, calls=%d", err, calls.Load())
	}
}
