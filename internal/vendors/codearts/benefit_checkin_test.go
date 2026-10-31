package codearts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

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
			result, err := Checkin(context.Background(), &Credential{AccessKeyID: "fixture-ak", SecretAccessKey: "fixture-sk", SecurityToken: "fixture-sts"})
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
