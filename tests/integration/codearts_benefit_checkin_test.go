package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"freetier2api-plugin/internal/vendors/codearts"
)

// 验证管理页复用的 /checkin 端点真正领取福利，并把完整结果写回历史。
func TestCodeArtsBenefitCheckinThroughManagement(t *testing.T) {
	for _, failBenefit := range []bool{false, true} {
		t.Run(fmt.Sprintf("benefitFailure=%v", failBenefit), func(t *testing.T) {
			host := installFakeHost(t)
			setupTestPlugin(t)
			host.addAccount("1", "codearts-user-1.json", codeartsCredentialJSON)
			var claims atomic.Int32
			host.setUpstream(func(method, url, body string) fakeUpstreamResponse {
				response := fakeUpstreamResponse{Status: http.StatusOK}
				switch {
				case strings.Contains(url, codearts.EpPackageInfo):
					response.Body = `{"code":0,"data":{"package":{"is_credit_package":true}}}`
				case strings.Contains(url, codearts.EpOpsDelivery):
					response.Body = fmt.Sprintf(`{"code":0,"data":{"items":[{"type":%q,"campaignId":1,"claimable":false,"status":"CLAIMED"}]}}`, codearts.OpsDailyLoginType)
				case strings.Contains(url, codearts.EpBenefitGateway):
					if failBenefit {
						response.Body = `{"error_code":"4004","error_msg":"benefit not found"}`
					} else {
						response.Body = `{"enabled":true}`
					}
				case strings.Contains(url, codearts.EpBenefitClaim):
					claims.Add(1)
					response.Body = `{"error_code":"0000","result":{"channel":"codearts","user_id":"fixture-user"}}`
				default:
					response.Status, response.Body = http.StatusNotFound, `{}`
				}
				return response
			})
			resp := managementCallWithBody(t, "POST", "/checkin", map[string]any{"account_ids": []string{"1"}})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("/checkin = %d: %s", resp.StatusCode, resp.Body)
			}
			var result struct {
				Results []struct {
					OK      bool   `json:"ok"`
					Message string `json:"message"`
				} `json:"results"`
			}
			if err := json.Unmarshal(resp.Body, &result); err != nil || len(result.Results) != 1 {
				t.Fatalf("invalid check-in response: %s (%v)", resp.Body, err)
			}
			entry := result.Results[0]
			if entry.OK == failBenefit || !strings.Contains(entry.Message, "今天已领取") {
				t.Fatalf("credit outcome must be preserved: %+v", entry)
			}
			wantMessage, wantStatus, wantClaims := "领取当日免费福利 Token", "success", int32(1)
			if failBenefit {
				wantMessage, wantStatus, wantClaims = "benefit not found", "error", 0
			}
			if !strings.Contains(entry.Message, wantMessage) || claims.Load() != wantClaims {
				t.Fatalf("unexpected benefit result: %+v, claims=%d", entry, claims.Load())
			}
			status := managementCall(t, "GET", "/status")
			var snapshot struct {
				Accounts []struct {
					Checkin struct {
						LastDate string `json:"last_date"`
					} `json:"checkin"`
					History []struct {
						Status  string `json:"status"`
						Message string `json:"message"`
					} `json:"checkin_history"`
				} `json:"accounts"`
			}
			if err := json.Unmarshal(status.Body, &snapshot); err != nil || len(snapshot.Accounts) != 1 {
				t.Fatalf("invalid status: %s (%v)", status.Body, err)
			}
			account := snapshot.Accounts[0]
			if len(account.History) != 1 || account.History[0].Status != wantStatus || account.History[0].Message != entry.Message {
				t.Fatalf("management outcome must round-trip through history: %+v", account)
			}
			if (account.Checkin.LastDate == "") != failBenefit {
				t.Fatalf("failed benefit must not mark the day complete: %+v", account)
			}
		})
	}
}
