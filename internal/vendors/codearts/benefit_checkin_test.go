package codearts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"freetier2api-plugin/internal/core"
	"freetier2api-plugin/internal/hostbridge"
)

// 使用宿主 HTTP 桥桩验证完整签到链路，不访问真实账号或领取真实权益。
func TestCheckinClaimsBenefitsIndependentlyOfCredits(t *testing.T) {
	available := fmt.Sprintf(`{"code":0,"data":{"items":[{"type":%q,"campaignId":1,"claimable":true,"amount":100}]}}`, OpsDailyLoginType)
	already := fmt.Sprintf(`{"code":0,"data":{"items":[{"type":%q,"campaignId":1,"claimable":false,"status":"CLAIMED"}]}}`, OpsDailyLoginType)
	for _, tc := range []struct {
		name           string
		account        string
		activity       string
		creditClaim    string
		gateway        string
		benefitClaim   string
		wantCredit     int64
		wantCreditCall bool
		wantBenefit    bool
		wantAlready    bool
		wantMessages   []string
		wantError      bool
	}{
		{
			name: "积分账户同时领取两份权益", activity: available,
			wantCredit: 100, wantCreditCall: true, wantBenefit: true,
			wantMessages: []string{"100", "领取当日免费福利 Token"},
		},
		{
			name: "积分已签到仍领取福利", activity: already, wantBenefit: true,
			wantMessages: []string{"今天已领取", "领取当日免费福利 Token"},
		},
		{
			name: "没有积分活动仍领取福利", activity: `{"code":0,"data":{"items":[]}}`, wantBenefit: true,
			wantMessages: []string{"未找到每日签到活动", "领取当日免费福利 Token"},
		},
		{
			name: "积分查询失败不阻断福利领取", activity: `{"code":7,"message":"credit campaign unavailable"}`,
			wantBenefit: true, wantError: true,
			wantMessages: []string{"credit campaign unavailable", "领取当日免费福利 Token"},
		},
		{
			name: "积分领取失败不阻断福利领取", activity: available,
			creditClaim:    `{"code":7,"message":"credit claim rejected"}`,
			wantCreditCall: true, wantBenefit: true, wantError: true,
			wantMessages: []string{"credit claim rejected", "领取当日免费福利 Token"},
		},
		{
			name: "福利未开放保留积分签到", activity: available, gateway: `{"enabled":false}`,
			wantCredit: 100, wantCreditCall: true, wantMessages: []string{"100"},
		},
		{
			name: "福利未开放保留积分已签到", activity: already, gateway: `{"enabled":false}`,
			wantAlready: true, wantMessages: []string{"今天已领取"},
		},
		{
			name: "福利名额用尽不得误报全部成功", activity: available,
			benefitClaim:   `{"error_code":"4006","error_msg":"4006"}`,
			wantCreditCall: true, wantBenefit: true, wantError: true,
			wantMessages: []string{"签到成功", "福利 Token 领取失败", "今日福利名额已用完"},
		},
		{
			name: "网关200业务错误不得当作未开放", activity: already,
			gateway: `{"error_code":"4004","error_msg":"benefit not found"}`, wantError: true,
			wantMessages: []string{"今天已领取", "benefit not found"},
		},
		{
			name: "领取空响应不得误报成功", activity: already, benefitClaim: `{}`,
			wantBenefit: true, wantError: true, wantMessages: []string{"missing result"},
		},
		{
			name: "两条权益都失败保留两份原因", activity: `{"code":7,"message":"credit unavailable"}`,
			benefitClaim: `{"error_code":"4006","error_msg":"4006"}`,
			wantBenefit:  true, wantError: true, wantMessages: []string{"credit unavailable", "今日福利名额已用完"},
		},
		{
			name: "Token账户仍可领取福利", account: `{"code":0,"data":{"package":{"is_token_package":true}}}`,
			wantBenefit: true, wantMessages: []string{"领取当日免费福利 Token"},
		},
		{
			name: "账户信息失败仍可领取福利", account: `{"code":7,"message":"account info unavailable"}`,
			wantBenefit: true, wantMessages: []string{"领取当日免费福利 Token"},
		},
		{
			name: "账户查询和福利都失败保留两份原因", account: `{"code":7,"message":"account info unavailable"}`,
			benefitClaim: `{"error_code":"4006","error_msg":"4006"}`,
			wantBenefit:  true, wantError: true, wantMessages: []string{"account info unavailable", "今日福利名额已用完"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responses := map[string]string{
				SnapEngineApiHost + EpPackageInfo:                            `{"code":0,"data":{"package":{"is_credit_package":true}}}`,
				SnapEngineApiHost + EpOpsDelivery + "?channel=" + OpsChannel: tc.activity,
				SnapEngineApiHost + EpOpsClaim:                               `{"code":0,"data":{"credit":100,"id":"fixture-benefit"}}`,
				SnapEngineApiHost + EpOpsConfirm:                             `{"code":0,"data":{}}`,
				SnapEngineApiHost + EpBenefitGateway:                         `{"enabled":true}`,
				BenefitHost + EpBenefitClaim:                                 `{"error_code":"0000","result":{"channel":"codearts","user_id":"fixture-user"}}`,
			}
			for url, body := range map[string]string{
				SnapEngineApiHost + EpPackageInfo:    tc.account,
				SnapEngineApiHost + EpOpsClaim:       tc.creditClaim,
				SnapEngineApiHost + EpBenefitGateway: tc.gateway,
				BenefitHost + EpBenefitClaim:         tc.benefitClaim,
			} {
				if body != "" {
					responses[url] = body
				}
			}
			calls := map[string]int{}
			hostbridge.Configure(func(_ string, method string, payload any) (json.RawMessage, error) {
				if method != "host.http.do" {
					return nil, fmt.Errorf("unexpected host method %s", method)
				}
				raw, _ := json.Marshal(payload)
				var req struct {
					URL     string              `json:"url"`
					Method  string              `json:"method"`
					Headers map[string][]string `json:"headers"`
					Body    []byte              `json:"body"`
				}
				if err := json.Unmarshal(raw, &req); err != nil {
					return nil, err
				}
				body, ok := responses[req.URL]
				if !ok || body == "" {
					return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
				}
				calls[req.URL]++
				headers := http.Header(req.Headers)
				if !strings.Contains(headers.Get("Authorization"), "SDK-HMAC-SHA256") || headers.Get("X-Security-Token") != "fixture-sts" {
					t.Errorf("missing account signature/STS for %s", req.URL)
				}
				if req.URL == BenefitHost+EpBenefitClaim && (req.Method != http.MethodGet || len(req.Body) != 0) {
					t.Errorf("benefit claim must use the existing signed GET protocol: %+v", req)
				}
				return json.Marshal(map[string]any{"StatusCode": 200, "Headers": map[string][]string{"Content-Type": {"application/json"}}, "Body": []byte(body)})
			})
			t.Cleanup(func() { hostbridge.Configure(nil) })
			result, err := checkin(context.Background(), &Credential{AccessKeyID: "fixture-ak", SecretAccessKey: "fixture-sk", SecurityToken: "fixture-sts"}, &benefitCheckinState{})
			if (err != nil) != tc.wantError {
				t.Fatalf("result=%+v error=%v, wantError=%v", result, err, tc.wantError)
			}
			var message string
			if tc.wantError {
				if result != nil {
					t.Fatalf("failed claim must not return a successful check-in: %+v", result)
				}
				message = err.Error()
			} else {
				if result == nil || result.NotRecorded || result.Credit != tc.wantCredit || result.Already != tc.wantAlready {
					t.Fatalf("unexpected result: %+v", result)
				}
				message = result.Message
			}
			for _, want := range tc.wantMessages {
				if !strings.Contains(message, want) {
					t.Errorf("message %q missing %q", message, want)
				}
			}
			for url, want := range map[string]bool{SnapEngineApiHost + EpOpsClaim: tc.wantCreditCall, BenefitHost + EpBenefitClaim: tc.wantBenefit} {
				count := 0
				if want {
					count = 1
				}
				if calls[url] != count {
					t.Errorf("%s: got %d calls, want %d", url, calls[url], count)
				}
			}
		})
	}
}

// TestMergeCheckinAlreadySemantics 锁住「已领」的合并口径：
// Already 只在两条链路本次都没有新增时为真。
func TestMergeCheckinAlreadySemantics(t *testing.T) {
	cases := []struct {
		name    string
		credit  *CheckinResult
		benefit *CheckinResult
		want    bool
		wantMsg string
	}{
		{
			name:   "积分无活动 + 福利今日已领",
			credit: &CheckinResult{Message: "未找到每日签到活动", NotRecorded: true},
			// 本轮什么都没领到：记 success 会让历史看起来签到成功而权益没到账。
			benefit: &CheckinResult{Already: true, Message: "今日免费福利 Token 已领取"},
			want:    true, wantMsg: "未找到每日签到活动；今日免费福利 Token 已领取",
		},
		{
			name:    "积分已签到 + 福利今日已领",
			credit:  &CheckinResult{Already: true, Message: "今天已领取"},
			benefit: &CheckinResult{Already: true, Message: "今日免费福利 Token 已领取"},
			want:    true,
		},
		{
			name:    "积分刚领到 + 福利今日已领",
			credit:  &CheckinResult{Credit: 100, Message: "领取 100 积分"},
			benefit: &CheckinResult{Already: true, Message: "今日免费福利 Token 已领取"},
			want:    false,
		},
		{
			name:    "两边都刚领到",
			credit:  &CheckinResult{Credit: 100, Message: "领取 100 积分"},
			benefit: &CheckinResult{Message: "已领取当日免费福利 Token"},
			want:    false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mergeCheckin(c.credit, c.benefit)
			if got.Already != c.want {
				t.Fatalf("Already = %v, want %v (%+v)", got.Already, c.want, got)
			}
			if got.NotRecorded {
				t.Fatal("福利侧返回结果即表示本轮有记录，NotRecorded 不应为真")
			}
			if c.wantMsg != "" && got.Message != c.wantMsg {
				t.Fatalf("Message = %q, want %q", got.Message, c.wantMsg)
			}
		})
	}
}

// TestCheckinFailureKeepsChainAndReadableMessage 锁住失败信息的两面性：
// 横幅只显示上游原文（不带 "upstream model_blocked [0]:" 这种内部分类前缀），
// 而 hosts 的 401 保活重试仍能通过 errors.As 拿到原始 *core.Failure。
func TestCheckinFailureKeepsChainAndReadableMessage(t *testing.T) {
	expired := &core.Failure{Kind: core.FailureKindCredential, Status: http.StatusUnauthorized, Msg: "APIG.0602 the security token has expired"}
	errJoin := newCheckinFailure("今天已领取", []checkinCause{
		{"积分签到失败", expired},
		{"福利 Token 领取失败", fmt.Errorf("claim benefit 4006: 今日福利名额已用完")},
	})

	message := errJoin.Error()
	if strings.Contains(message, "upstream ") {
		t.Fatalf("横幅不应出现内部分类前缀: %q", message)
	}
	for _, want := range []string{"今天已领取", "积分签到失败", "APIG.0602", "今日福利名额已用完"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q missing %q", message, want)
		}
	}
	var failure *core.Failure
	if !errors.As(errJoin, &failure) || failure != expired {
		t.Fatalf("401 必须仍可被 errors.As 找到（保活重试依赖它）: %v", errJoin)
	}
}

// 同日成功后由本地成功证据短路，不依赖上游是否允许重复领取。
// 未确认的 4006 不得被猜成 already，失败后仍允许下一次人工/定时重试。
func TestBenefitDailyGuardAndFailedRetry(t *testing.T) {
	for _, firstFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("firstFails=%v", firstFails), func(t *testing.T) {
			var claims atomic.Int32
			stubBenefitClaims(t, &claims, firstFails)
			cred := &Credential{AccessKeyID: "ak", SecretAccessKey: "sk"}
			clock := time.Date(2026, 10, 31, 15, 59, 0, 0, time.UTC) // 北京时间 23:59。
			state := &benefitCheckinState{now: func() time.Time { return clock }}
			first, err := state.claimAvailable(context.Background(), cred)
			if firstFails {
				if err == nil || first != nil || state.claimedOn != "" {
					t.Fatalf("4006 must remain an unrecorded failure: %+v %v", first, err)
				}
			} else if err != nil || first.Already {
				t.Fatalf("first claim: %+v %v", first, err)
			}
			second, err := state.claimAvailable(context.Background(), cred)
			if err != nil || second.Already == firstFails {
				t.Fatalf("retry/guard: %+v %v", second, err)
			}
			wantCalls := int32(1)
			if firstFails {
				wantCalls = 2
			}
			if claims.Load() != wantCalls {
				t.Fatalf("claims=%d, want %d", claims.Load(), wantCalls)
			}
			clock = clock.Add(2 * time.Minute) // UTC 日期不变，但北京时间已跨天。
			next, err := state.claimAvailable(context.Background(), cred)
			if err != nil || next.Already || claims.Load() != wantCalls+1 {
				t.Fatalf("new Beijing day must claim again: %+v %v calls=%d", next, err, claims.Load())
			}
		})
	}
}

func TestBenefitConcurrentClaimsAreCoalesced(t *testing.T) {
	var claims atomic.Int32
	stubBenefitClaims(t, &claims, false)
	state := &benefitCheckinState{now: func() time.Time { return time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC) }}
	cred := &Credential{AccessKeyID: "ak", SecretAccessKey: "sk"}
	var wait sync.WaitGroup
	var already atomic.Int32
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := state.claimAvailable(context.Background(), cred)
			if err != nil || result == nil {
				t.Errorf("claim: %+v %v", result, err)
				return
			}
			if result.Already {
				already.Add(1)
			}
		}()
	}
	wait.Wait()
	if claims.Load() != 1 || already.Load() != 7 {
		t.Fatalf("expected one claim and seven already results: claims=%d already=%d", claims.Load(), already.Load())
	}
}

func stubBenefitClaims(t *testing.T, claims *atomic.Int32, firstFails bool) {
	t.Helper()
	hostbridge.Configure(func(_ string, method string, payload any) (json.RawMessage, error) {
		raw, _ := json.Marshal(payload)
		var req struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(raw, &req)
		body := `{"enabled":true}`
		if req.URL == BenefitHost+EpBenefitClaim {
			count := claims.Add(1)
			body = `{"error_code":"0000","result":{"channel":"codearts","user_id":"fixture"}}`
			if firstFails && count == 1 {
				body = `{"error_code":"4006","error_msg":"4006"}`
			}
		} else if req.URL != SnapEngineApiHost+EpBenefitGateway {
			return nil, fmt.Errorf("unexpected URL %s", req.URL)
		}
		return json.Marshal(map[string]any{"StatusCode": 200, "Body": []byte(body)})
	})
	t.Cleanup(func() { hostbridge.Configure(nil) })
}

// TestCheckinAuthExpiredDoesNotFallbackToBenefit 验证当账户查询报 401 凭证过期时，
// 绝不能错误地 fallback 调福利接口（那会导致解密失败 APIG.0301 并覆盖真实错误），
// 必须直接返回 401 凭证错误，以供上层刷新重试。
func TestCheckinAuthExpiredDoesNotFallbackToBenefit(t *testing.T) {
	calls := map[string]int{}
	hostbridge.Configure(func(_ string, method string, payload any) (json.RawMessage, error) {
		raw, _ := json.Marshal(payload)
		var req struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(raw, &req)
		calls[req.URL]++
		if req.URL == SnapEngineApiHost+EpPackageInfo {
			return json.Marshal(map[string]any{
				"StatusCode": 401,
				"Body":       []byte(`{"error_code":"APIG.0602","error_msg":"Bad request: the security token has expired"}`),
			})
		}
		return nil, fmt.Errorf("unexpected URL: %s", req.URL)
	})
	t.Cleanup(func() { hostbridge.Configure(nil) })

	cred := &Credential{AccessKeyID: "expired-ak", SecretAccessKey: "expired-sk", SecurityToken: "expired-sts"}
	result, err := checkin(context.Background(), cred, &benefitCheckinState{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if result != nil {
		t.Fatalf("expected nil result, got %+v", result)
	}
	if !core.IsAuthExpired(err) {
		t.Fatalf("expected IsAuthExpired to be true, got error: %v", err)
	}
	if strings.Contains(err.Error(), "福利 Token 领取失败") {
		t.Fatalf("auth expired error should not trigger benefit fallback: %v", err)
	}
	if calls[BenefitHost+EpBenefitClaim] > 0 || calls[SnapEngineApiHost+EpBenefitGateway] > 0 {
		t.Fatalf("must not call benefit endpoints when auth is expired")
	}
}

// TestParseExpirationAndValid 验证 Expiration 时间解析与 Valid 方法
func TestParseExpirationAndValid(t *testing.T) {
	cred, err := ParseCredential([]byte(`{
		"access_key_id": "ak",
		"secret_access_key": "sk",
		"expiration": "2026-11-01T15:00:00.000Z"
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wantExp, _ := time.Parse(time.RFC3339, "2026-11-01T15:00:00.000Z")
	if cred.ExpiresAt != wantExp.Unix() {
		t.Fatalf("ExpiresAt = %d, want %d", cred.ExpiresAt, wantExp.Unix())
	}
	futureCred := &Credential{
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
		ExpiresAt:       time.Now().Add(10 * time.Minute).Unix(),
	}
	if !futureCred.Valid(5 * time.Minute) {
		t.Fatal("10 分钟后到期的凭据在 5 分钟提前量下应为 Valid")
	}
	if futureCred.Valid(15 * time.Minute) {
		t.Fatal("10 分钟后到期的凭据在 15 分钟提前量下不应为 Valid")
	}
}

// TestBuildDPoPProofDerivesPublicFromPrivate 验证 DPoP 仅有私钥时自动派生公钥
func TestBuildDPoPProofDerivesPublicFromPrivate(t *testing.T) {
	jwk, err := generateDPoPJWK()
	if err != nil {
		t.Fatal(err)
	}
	delete(jwk, "x")
	delete(jwk, "y")
	proof, err := buildDPoPProof(jwk, "https://example.com/tokens")
	if err != nil {
		t.Fatalf("buildDPoPProof with missing x/y failed: %v", err)
	}
	if !strings.HasPrefix(proof, "eyJ") {
		t.Fatalf("proof does not look like JWT: %s", proof)
	}
	if jwk["x"] == "" || jwk["y"] == "" {
		t.Fatalf("expected x and y to be derived into jwk")
	}
}
