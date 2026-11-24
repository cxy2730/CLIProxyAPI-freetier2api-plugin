package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"freetier2api-plugin/cpasdk/pluginabi"
	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/hostbridge"
)

func TestCreditCooldownRetryHint(t *testing.T) {
	now := time.Now()
	reset := now.Add(3 * time.Hour).In(softRateResetLoc).Format(softRateTimeLayout)
	stale := now.Add(-time.Hour).In(softRateResetLoc).Format(softRateTimeLayout)
	const depleted = "{\"code\":14018,\"msg\":\"insufficient credit\"}"
	for _, test := range []struct {
		name   string
		status int
		body   string
		header http.Header
		want   int
		code   string
	}{
		{"explicit reset wins", 429, fmt.Sprintf("{\"code\":14018,\"msg\":\"reset at %s\"}", reset), http.Header{"Retry-After": {"60"}}, 10800, "upstream_hard_credit"},
		{"past reset uses hint", 429, fmt.Sprintf("{\"code\":14018,\"msg\":\"reset at %s\"}", stale), http.Header{"Retry-After": {"60"}}, 60, "upstream_hard_credit"},
		{"unknown reset", 429, depleted, nil, 1800, "upstream_hard_credit"},
		{"payment required", 402, depleted, nil, 1800, "upstream_hard_credit"},
		{"ordinary rate limit", 429, "{\"code\":6004,\"msg\":\"quota exceeded\"}", nil, 0, "upstream_soft_rate"},
		{"ordinary rate limit with hint", 429, "{\"code\":6004}", http.Header{"Retry-After": {"12"}}, 12, "upstream_soft_rate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			v := &adapter{}
			account := creditAccount{fileID: "workbuddy-test"}
			upstream := enrichClassifyError(test.status, test.body, test.header)
			err := v.handleChatError(account, nil, "model-a", upstream)
			detail := core.ToPluginError(err).(*core.PluginError)
			if detail.HTTPStatus != http.StatusTooManyRequests || detail.Code != test.code ||
				detail.RetryAfterSeconds > test.want || detail.RetryAfterSeconds < max(0, test.want-1) {
				t.Fatalf("unexpected host error: %+v", *detail)
			}
			if got := v.checkCreditCooldown(account) != nil; got != (test.code == "upstream_hard_credit") {
				t.Fatalf("credential cooling = %t", got)
			}
		})
	}
}

func TestCreditCooldownAppliesAcrossModels(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var upstreamCalls atomic.Int32
			var reads atomic.Int32
			hostbridge.Configure(func(_ string, method string, _ any) (json.RawMessage, error) {
				switch method {
				case pluginabi.MethodHostHTTPDoStream:
					upstreamCalls.Add(1)
					reads.Store(0)
					if stream {
						return json.RawMessage("{\"status_code\":200,\"stream_id\":\"quota\"}"), nil
					}
					return json.RawMessage("{\"status_code\":429,\"stream_id\":\"quota\"}"), nil
				case pluginabi.MethodHostHTTPStreamRead:
					if reads.Add(1) > 1 {
						return json.RawMessage("{\"done\":true}"), nil
					}
					body := "{\"error\":{\"code\":14018,\"message\":\"insufficient credit\"}}"
					if stream {
						body = "data: " + body + "\n\n"
					}
					return json.Marshal(map[string]any{"payload": []byte(body)})
				case pluginabi.MethodHostHTTPStreamClose:
					return json.RawMessage("{}"), nil
				default:
					return nil, fmt.Errorf("unexpected host method %q", method)
				}
			})
			t.Cleanup(func() { hostbridge.Configure(nil) })

			v := newInstance(core.Env{}, RegionCN).(*adapter)
			native := testTaskCredential("uid")
			cred := &core.Credential{FileID: "depleted", Native: native}
			other := &core.Credential{FileID: "other", Native: testTaskCredential("other")}
			request := func(model string) *core.ExecuteRequest {
				return &core.ExecuteRequest{Model: model, Payload: []byte("{\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")}
			}
			checkError := func(err error) {
				t.Helper()
				var failure *core.Failure
				if !errors.As(err, &failure) || failure.Kind != core.FailureKindHardCredit || failure.RetryAfter() <= 0 {
					t.Fatalf("expected credit cooldown, got %v", err)
				}
			}

			ctx := context.Background()
			sink := creditTestSink{t: t}
			if stream {
				checkError(v.ExecuteStream(ctx, cred, request("model-a"), sink))
			} else {
				_, err := v.Execute(ctx, cred, request("model-a"))
				checkError(err)
			}
			key := creditAccount{fileID: cred.FileIDValue(), uid: native.UIDValue()}
			deadline := v.creditCooldown[key]
			checkError(v.ExecuteStream(ctx, cred, request("model-b"), sink))
			if upstreamCalls.Load() != 1 || !v.creditCooldown[key].Equal(deadline) {
				t.Fatal("sibling model called upstream or extended the deadline")
			}
			_, err := v.Execute(ctx, other, request("model-b"))
			checkError(err)
			if upstreamCalls.Load() != 2 {
				t.Fatal("another credential was blocked by the first")
			}
			v.creditCooldown[key] = time.Now().Add(-time.Second)
			_, err = v.Execute(ctx, cred, request("model-a"))
			checkError(err)
			if upstreamCalls.Load() != 3 {
				t.Fatal("credential did not become eligible when the cooldown expired")
			}
		})
	}
}

type creditTestSink struct{ t *testing.T }

func (s creditTestSink) Close(string) error { return nil }

func (s creditTestSink) Emit(payload []byte) error {
	s.t.Fatalf("credit errors must be returned to the host, not emitted as successful chunks: %s", payload)
	return nil
}
