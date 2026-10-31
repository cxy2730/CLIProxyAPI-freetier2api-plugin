package trae

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 活动福利领取的行为测试。
//
// 这类端点的失败原因全写在 body 的 message 里，状态码经常还是 200：把
// 「已领取」当成「失败」会让人反复重试，把「活动已结束」当成「已领取」又会
// 掩盖真的没领到。所以归类逻辑必须逐个码位固定下来。

// ugTestCredential 造一个国内版凭证（uid 决定派生的设备头）。
func ugTestCredential() *Credential {
	return &Credential{
		Vendor:      VendorIDCN,
		Region:      RegionCN,
		AccessToken: "test-access-token",
		UID:         "2487573687317868",
	}
}

// ugTestConfig 指向假上游。
//
// 插件不发真实请求，一律经宿主 http.do 回调，所以用例必须先 stubHost。
func ugTestConfig(t *testing.T, host string) Config {
	t.Helper()
	return testConfig(t, host)
}

// TestUGActionsRedeemNeedsInput 验证：六个入口里只有兑换码需要用户输入。
func TestUGActionsRedeemNeedsInput(t *testing.T) {
	actions := UGActions()
	if len(actions) != 6 {
		t.Fatalf("活动入口数 = %d, 期望 6", len(actions))
	}
	var needInput []string
	for _, action := range actions {
		if action.NeedsInput {
			needInput = append(needInput, action.Key)
		}
		if action.Path == "" || action.Label == "" {
			t.Errorf("%s: path/label 不能为空", action.Key)
		}
	}
	if len(needInput) != 1 || needInput[0] != "redeem_code" {
		t.Errorf("需要输入的入口 = %v, 期望只有 redeem_code", needInput)
	}
}

// TestClaimUGClassifiesEachEndpoint 验证：逐个端点的归类与「兑换码不参与批量」。
func TestClaimUGClassifiesEachEndpoint(t *testing.T) {
	replies := map[string]struct {
		status int
		body   string
	}{
		EpUGCompetitionClaim: {200, `{"code":0,"message":"ok"}`},
		EpUGCobrandClaim:     {200, `{"code":1001,"message":"该奖励已领取"}`},
		EpUGWorkLotteryDraw:  {200, `{"code":2002,"message":"活动已结束"}`},
		EpUGWorkFissionShare: {500, `{"code":5001,"message":"服务繁忙"}`},
		EpUGCodeFissionShare: {200, `{"code":3003,"message":"参数错误"}`},
	}
	stubHost(t)
	var hits []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("%s: method = %s, 期望 POST", r.URL.Path, r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Cloud-IDE-JWT test-access-token" {
			t.Errorf("%s: Authorization = %q", r.URL.Path, got)
		}
		if got := r.Header.Get("X-Market-User-Id"); got == "" {
			t.Errorf("%s: 缺少派生的 X-Market-User-Id", r.URL.Path)
		}
		hits = append(hits, r.URL.Path)
		reply, ok := replies[r.URL.Path]
		if !ok {
			// 兑换码不应出现在批量领取里。
			t.Errorf("批量领取打到了 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	defer server.Close()

	results, errClaim := ClaimUG(context.Background(), ugTestCredential(), ugTestConfig(t, server.URL))
	if errClaim != nil {
		t.Fatalf("ClaimUG 返回错误: %v", errClaim)
	}
	if len(results) != 5 {
		t.Fatalf("结果数 = %d, 期望 5（兑换码不参与批量）", len(results))
	}
	want := map[string]UGStatus{
		"competition_claim":  UGClaimed,
		"cobrand_claim":      UGAlready,
		"work_lottery_draw":  UGUnavailable,
		"work_fission_share": UGFailed,
		"code_fission_share": UGFailed,
	}
	for _, result := range results {
		if status, ok := want[result.Key]; ok && result.Status != status {
			t.Errorf("%s: status = %d, 期望 %d（文案 %q）", result.Key, result.Status, status, result.Message)
		}
	}
	for _, path := range hits {
		if path == EpPayRedeemCode {
			t.Error("兑换码端点不应被批量领取调用")
		}
	}
}

// TestClaimUGAbortsOnCancelledContext 验证：取消后不再继续打后面的端点。
func TestClaimUGAbortsOnCancelledContext(t *testing.T) {
	stubHost(t)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, errClaim := ClaimUG(ctx, ugTestCredential(), ugTestConfig(t, server.URL))
	if errClaim == nil {
		t.Fatal("取消后应返回错误")
	}
	if calls != 0 {
		t.Errorf("取消后仍打了 %d 个端点", calls)
	}
	if len(results) != 0 {
		t.Errorf("取消后不应有结果，得到 %d 条", len(results))
	}
}

// TestClaimUGRejectsMissingIdentity 验证：没有 uid 的凭证不派发共享设备身份。
func TestClaimUGRejectsMissingIdentity(t *testing.T) {
	credential := ugTestCredential()
	credential.UID = ""
	if _, errClaim := ClaimUG(context.Background(), credential, ugTestConfig(t, "https://example.invalid")); errClaim == nil {
		t.Fatal("缺少 uid 时应报错")
	}
}

// TestClaimUGRejectsGlobalRegion 验证：国际版没有这套活动。
func TestClaimUGRejectsGlobalRegion(t *testing.T) {
	credential := ugTestCredential()
	credential.Region = RegionGlobal
	if _, errClaim := ClaimUG(context.Background(), credential, ConfigFor(RegionGlobal)); errClaim == nil {
		t.Fatal("国际版应拒绝活动福利领取")
	}
}

// TestRedeemUGCodePostsCode 验证：兑换码走单独入口并带上 redeem_code。
func TestRedeemUGCodePostsCode(t *testing.T) {
	stubHost(t)
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"message":"兑换成功"}`))
	}))
	defer server.Close()

	result, errRedeem := RedeemUGCode(context.Background(), ugTestCredential(), ugTestConfig(t, server.URL), "  ABC-123 ")
	if errRedeem != nil {
		t.Fatalf("兑换返回错误: %v", errRedeem)
	}
	if gotPath != EpPayRedeemCode {
		t.Errorf("path = %q, 期望 %q", gotPath, EpPayRedeemCode)
	}
	if code, _ := gotBody["redeem_code"].(string); code != "ABC-123" {
		t.Errorf("redeem_code = %v, 期望 ABC-123（两端空白应被裁掉）", gotBody["redeem_code"])
	}
	if result.Status != UGClaimed {
		t.Errorf("status = %d, 期望 UGClaimed", result.Status)
	}
}

// TestRedeemUGCodeRejectsEmpty 验证：空兑换码不发请求。
func TestRedeemUGCodeRejectsEmpty(t *testing.T) {
	if _, errRedeem := RedeemUGCode(context.Background(), ugTestCredential(), ugTestConfig(t, "https://example.invalid"), "   "); errRedeem == nil {
		t.Fatal("空兑换码应报错")
	}
}

// TestSummarizeUGCounts 验证：汇总文案的四类计数与逐条文案。
func TestSummarizeUGCounts(t *testing.T) {
	results := []UGResult{
		{Key: "a", Label: "竞赛奖励", Status: UGClaimed},
		{Key: "b", Label: "联合活动奖励", Status: UGAlready},
		{Key: "c", Label: "作品抽奖", Status: UGUnavailable, Message: "活动已结束"},
		{Key: "d", Label: "作品分享奖励", Status: UGFailed, Message: "HTTP 500"},
	}
	text := SummarizeUG(results)
	for _, want := range []string{"领到 1 项", "此前已领 1 项", "暂不可领 1 项", "失败 1 项", "竞赛奖励：已领取"} {
		if !strings.Contains(text, want) {
			t.Errorf("汇总文案缺少 %q：%s", want, text)
		}
	}
	if got := SummarizeUG(nil); got != "没有可领取的活动入口" {
		t.Errorf("空结果文案 = %q", got)
	}
}

// TestClassifyUGMessage 固定归类口径：码表未知，只按文案粗分。
func TestClassifyUGMessage(t *testing.T) {
	cases := []struct {
		message string
		want    UGStatus
	}{
		{"该奖励已领取", UGAlready},
		{"你已经参与过该活动", UGAlready},
		{"活动已结束", UGUnavailable},
		{"活动未开始", UGUnavailable},
		{"当前账号不满足参与条件", UGUnavailable},
		{"参数错误", UGFailed},
		{"", UGFailed},
	}
	for _, item := range cases {
		if got := classifyUGMessage(item.message); got != item.want {
			t.Errorf("classifyUGMessage(%q) = %d, 期望 %d", item.message, got, item.want)
		}
	}
}

// TestTaskSpecsFollowRegion 验证：只有国内版声明活动福利族，且是手动族。
//
// 不带 DefaultHours 是刻意的：这些入口只在活动期有东西可领，默认排程只会
// 每天白打 6 次空请求。
func TestTaskSpecsFollowRegion(t *testing.T) {
	cn := &adapter{config: ConfigFor(RegionCN)}
	specs := cn.TaskSpecs()
	if len(specs) != 1 || specs[0].Kind != TaskKindUG {
		t.Fatalf("国内版任务族 = %+v, 期望只有 %s", specs, TaskKindUG)
	}
	if !specs[0].Manual {
		t.Error("活动福利必须是手动族")
	}
	if len(specs[0].DefaultHours) != 0 {
		t.Errorf("手动族不应带默认排程，得到 %v", specs[0].DefaultHours)
	}
	if specs[0].Label == "" || specs[0].Description == "" {
		t.Error("任务族必须带中文标签与说明")
	}

	global := &adapter{config: ConfigFor(RegionGlobal)}
	if specsGlobal := global.TaskSpecs(); len(specsGlobal) != 0 {
		t.Errorf("国际版不应声明任务族，得到 %+v", specsGlobal)
	}
}

// TestRunScheduledRejectsUnknownKind 验证：未知任务族直接报错，不静默成功。
func TestRunScheduledRejectsUnknownKind(t *testing.T) {
	cn := &adapter{config: ConfigFor(RegionCN)}
	if _, errRun := cn.RunScheduled(context.Background(), nil, "workbuddy_dailybonus"); errRun == nil {
		t.Fatal("未知任务族应报错")
	}
}
