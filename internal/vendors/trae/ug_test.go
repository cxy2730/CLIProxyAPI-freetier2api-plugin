package trae

import (
	"context"
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

// TestUGActionsAreBatchable 验证：批量入口都是「无需输入、可直接 POST」的。
//
// 兑换码需要用户提供内容，而手动触发路由只传任务族名、没有请求体入口，所以
// 那条链路已删除——留着它只会是一个永远调不到的实现。
//
// cobrand/claim 与两个 share_link 需要活动 ID / 作品 ID（上游实测回
// 4001 / 9080「param is invalid」），插件取不到，同样不进批量入口：每轮白打
// 3 次注定失败的请求，只会拖慢整轮并把真实原因糊成「暂不可领」。
func TestUGActionsAreBatchable(t *testing.T) {
	actions := UGActions()
	if len(actions) != 2 {
		t.Fatalf("活动入口数 = %d, 期望 2（竞赛奖励 + 作品抽奖）", len(actions))
	}
	for _, excluded := range []string{EpUGCobrandClaim, EpUGWorkFissionShare, EpUGCodeFissionShare} {
		for _, action := range actions {
			if action.Path == excluded {
				t.Errorf("需要入参的端点不该进批量入口: %s", excluded)
			}
		}
	}
	seen := map[string]bool{}
	for _, action := range actions {
		if action.Path == "" || action.Label == "" || action.Key == "" {
			t.Errorf("%s: key/path/label 不能为空", action.Key)
		}
		if seen[action.Key] {
			t.Errorf("入口 key 重复: %s", action.Key)
		}
		seen[action.Key] = true
	}
}

// TestClaimUGClassifiesEachEndpoint 验证：逐个端点的归类。
func TestClaimUGClassifiesEachEndpoint(t *testing.T) {
	type reply struct {
		status int
		body   string
	}
	// 批量入口只剩两个端点，所以分两轮跑：一轮覆盖「领到 / 活动关闭」，
	// 一轮覆盖「已领取 / 缺参」。第二轮在 lottery 上借用 share_link 的
	// 缺参码 4001，是为了把 ugParamMarkers 那条路径也走到（归类逻辑同源，
	// 与端点无关）。
	rounds := []struct {
		name    string
		replies map[string]reply
		want    map[string]UGStatus
	}{
		{
			name: "正常发放",
			replies: map[string]reply{
				EpUGCompetitionClaim: {200, `{"code":0,"message":"ok"}`},
				EpUGWorkLotteryDraw:  {200, `{"code":2002,"message":"活动已结束"}`},
			},
			want: map[string]UGStatus{
				"competition_claim": UGClaimed,
				"work_lottery_draw": UGUnavailable,
			},
		},
		{
			name: "已领取与缺参",
			replies: map[string]reply{
				EpUGCompetitionClaim: {200, `{"code":1001,"message":"该奖励已领取"}`},
				EpUGWorkLotteryDraw:  {200, `{"code":4001,"message":"param is invalid"}`},
			},
			want: map[string]UGStatus{
				"competition_claim": UGAlready,
				"work_lottery_draw": UGUnavailable,
			},
		},
	}

	for _, round := range rounds {
		replies := round.replies
		stubHost(t)
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
			item, ok := replies[r.URL.Path]
			if !ok {
				t.Errorf("批量领取打到了未知端点 %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(item.status)
			_, _ = w.Write([]byte(item.body))
		}))

		results, errClaim := ClaimUG(context.Background(), ugTestCredential(), ugTestConfig(t, server.URL))
		server.Close()
		if errClaim != nil {
			t.Fatalf("%s: ClaimUG 返回错误: %v", round.name, errClaim)
		}
		if len(results) != 2 {
			t.Fatalf("%s: 结果数 = %d, 期望 2", round.name, len(results))
		}
		for _, result := range results {
			status, known := round.want[result.Key]
			if !known {
				t.Errorf("%s: 出现预期外的入口 key: %s", round.name, result.Key)
				continue
			}
			if result.Status != status {
				t.Errorf("%s: %s status = %d, 期望 %d（文案 %q）",
					round.name, result.Key, result.Status, status, result.Message)
			}
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

// TestClaimUGTreatsNonJSONAsFailure 锁住「200 + 非 JSON ≠ 已领取」。
//
// 网关超时页、空 body、{"data":{}} 都没有 code 字段；按零值当成 code=0 会把
// 失败报成「已领取」，用户以为领到了，实际什么都没有。
func TestClaimUGTreatsNonJSONAsFailure(t *testing.T) {
	for _, body := range []string{"<html>502 Bad Gateway</html>", "", `{"data":{}}`} {
		stubHost(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
		}))
		results, errClaim := ClaimUG(context.Background(), ugTestCredential(), ugTestConfig(t, server.URL))
		server.Close()
		if errClaim != nil {
			t.Fatalf("body=%q: 不该中止整轮: %v", body, errClaim)
		}
		for _, result := range results {
			if result.Status != UGFailed {
				t.Fatalf("body=%q: %s status = %d, 期望 UGFailed", body, result.Key, result.Status)
			}
			if strings.TrimSpace(result.Message) == "" {
				t.Fatalf("body=%q: %s 失败时必须带原因", body, result.Key)
			}
		}
	}
}

// TestClaimUGStopsOnUnauthorized 锁住「401 立刻停」：后面的端点只会得到同一个
// 401，继续打只会让日志更难读；已拿到的结果仍要带出去。
func TestClaimUGStopsOnUnauthorized(t *testing.T) {
	stubHost(t)
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":1001,"message":"unauthorized"}`))
	}))
	defer server.Close()

	results, errClaim := ClaimUG(context.Background(), ugTestCredential(), ugTestConfig(t, server.URL))
	if errClaim == nil {
		t.Fatal("401 应中止整轮并上报错误")
	}
	if calls != 1 {
		t.Fatalf("401 后不应继续请求，实际打了 %d 个端点", calls)
	}
	if len(results) != 1 || results[0].Status != UGFailed {
		t.Fatalf("应带回首个失败结果: %+v", results)
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
		{"活动暂不可用", UGUnavailable},
		{"param is invalid", UGUnavailable},
		{"参数错误", UGUnavailable},
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
	// 宿主只为「Route 非空」的手动族注册管理路由；没有 Route 的手动族在控制台
	// 根本没有触发入口，等于声明了却做不到。
	if specs[0].Route == "" {
		t.Error("手动族必须带 Route，否则控制台没有触发入口")
	}
	if TaskKindUG != "ug" {
		t.Errorf("族名 = %q, 期望与相邻 vendor 一致的 %q", TaskKindUG, "ug")
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

// TestClaimUGReadsNoCodeState 锁住「无业务码响应按状态字段归类」。
//
// 实测 claim_package 回 {"eligible":false,...}：没有 code。按 code==nil 一律
// 归失败，会把「当前账号不满足条件」报成上游故障，用户只能看到一串无意义的
// 失败横幅。
func TestClaimUGReadsNoCodeState(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status UGStatus
	}{
		{"资格不足-顶层", `{"eligible":false,"message":"no quota"}`, UGUnavailable},
		{"资格不足-嵌套", `{"data":{"eligible":false}}`, UGUnavailable},
		{"本次已发放", `{"eligible":true,"newly_granted":true}`, UGClaimed},
		{"可参与但未发放", `{"eligible":true,"newly_granted":false}`, UGUnavailable},
		// 两个字段同时出现且互相矛盾：以「本次发放成功」为准。上游真的发了
		// 东西，却因为 eligible=false 报「不满足条件」，等于把领到的奖励吞掉。
		{"资格不足但本次已发放", `{"eligible":false,"newly_granted":true}`, UGClaimed},
		// 只给 newly_granted=false，没有 eligible：本次没发东西，但不是
		// 「不满足条件」，文案不能张冠李戴。
		{"仅本次未发放", `{"newly_granted":false}`, UGUnavailable},
		{"本次已发放-嵌套", `{"data":{"eligible":false,"newly_granted":true}}`, UGClaimed},
	}
	for _, item := range cases {
		stubHost(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(item.body))
		}))
		results, errClaim := ClaimUG(context.Background(), ugTestCredential(), ugTestConfig(t, server.URL))
		server.Close()
		if errClaim != nil {
			t.Fatalf("%s: 不该中止整轮: %v", item.name, errClaim)
		}
		for _, result := range results {
			if result.Status != item.status {
				t.Fatalf("%s: %s status = %d, 期望 %d", item.name, result.Key, result.Status, item.status)
			}
			if strings.TrimSpace(result.Message) == "" {
				t.Fatalf("%s: %s 必须带原因", item.name, result.Key)
			}
			// 这条路径上游没给业务码，Code 是零值占位；标记必须置上，
			// 否则后续按 Code==0 统计会把这些当成「上游回 0 = 已领取」。
			if !result.NoCode {
				t.Fatalf("%s: %s 缺 NoCode 标记", item.name, result.Key)
			}
		}
	}
}

// TestClaimUGClassifiesMissingParamOnNon200 锁住「非 200 的缺参数也归暂不可领」。
//
// 实测 cobrand/claim 缺 activity_id 回 400 + code 9080「param is invalid」。
func TestClaimUGClassifiesMissingParamOnNon200(t *testing.T) {
	stubHost(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":9080,"message":"param is invalid"}`))
	}))
	defer server.Close()

	results, errClaim := ClaimUG(context.Background(), ugTestCredential(), ugTestConfig(t, server.URL))
	if errClaim != nil {
		t.Fatalf("400 不该中止整轮: %v", errClaim)
	}
	for _, result := range results {
		if result.Status != UGUnavailable {
			t.Fatalf("%s status = %d, 期望 UGUnavailable（文案 %q）", result.Key, result.Status, result.Message)
		}
		if result.Code != 9080 {
			t.Errorf("%s code = %d, 期望原样带出 9080", result.Key, result.Code)
		}
	}
}

// TestClaimUGKeepsServerErrorsAsFailure 锁住「5xx / 429 不能被文案折成暂不可领」。
//
// 这是本函数原先的缺陷：非 200 分支只要文案命中「暂不可用」就归 UGUnavailable，
// 于是一个 503（上游真的挂了）会被显示成「活动暂不可领」，调度不会重试、用户
// 也不会，故障被静默吞掉。归类只对 4xx 成立；5xx 与 429 必须留给 Classify。
func TestClaimUGKeepsServerErrorsAsFailure(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"503-文案像活动关闭", http.StatusServiceUnavailable, `{"code":5001,"message":"活动暂不可用"}`},
		{"503-纯网关页", http.StatusBadGateway, `<html>502 Bad Gateway</html>`},
		{"500-缺参文案", http.StatusInternalServerError, `{"code":9080,"message":"param is invalid"}`},
		{"429-已领取文案", http.StatusTooManyRequests, `{"code":1001,"message":"该奖励已领取"}`},
	}
	for _, item := range cases {
		stubHost(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(item.status)
			_, _ = w.Write([]byte(item.body))
		}))
		results, errClaim := ClaimUG(context.Background(), ugTestCredential(), ugTestConfig(t, server.URL))
		server.Close()
		if errClaim != nil {
			t.Fatalf("%s: 不该中止整轮: %v", item.name, errClaim)
		}
		if len(results) != 2 {
			t.Fatalf("%s: 结果数 = %d, 期望 2", item.name, len(results))
		}
		for _, result := range results {
			if result.Status != UGFailed {
				t.Errorf("%s: %s status = %d, 期望 UGFailed（文案 %q）", item.name, result.Key, result.Status, result.Message)
			}
			if strings.TrimSpace(result.Message) == "" {
				t.Errorf("%s: %s 失败时必须带原因", item.name, result.Key)
			}
		}
	}
}

// TestClaimUGClassifiesAlreadyOnNon200 锁住「4xx 的已领取也归已领取」。
//
// 实测幂等端点对重复领取回 409 + 1001「该奖励已领取」。只认 UGUnavailable
// 会让这种正常结果掉进「失败」，用户看到红字却什么都没坏。
func TestClaimUGClassifiesAlreadyOnNon200(t *testing.T) {
	stubHost(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":1001,"message":"该奖励已领取"}`))
	}))
	defer server.Close()

	results, errClaim := ClaimUG(context.Background(), ugTestCredential(), ugTestConfig(t, server.URL))
	if errClaim != nil {
		t.Fatalf("409 不该中止整轮: %v", errClaim)
	}
	for _, result := range results {
		if result.Status != UGAlready {
			t.Errorf("%s status = %d, 期望 UGAlready（文案 %q）", result.Key, result.Status, result.Message)
		}
		if result.Code != 1001 {
			t.Errorf("%s code = %d, 期望原样带出 1001", result.Key, result.Code)
		}
	}
}

// TestReadUGState 验证：没有状态字段时必须退回失败归类，不能被当成可领取。
func TestReadUGState(t *testing.T) {
	for _, body := range []string{`{"data":{}}`, `<html>502</html>`, `{}`, `null`} {
		if _, okState := readUGState([]byte(body)); okState {
			t.Errorf("body=%q: 不该识别出状态字段", body)
		}
	}
}

// TestUGDetailText 验证：机器文案翻成中文提示，认不出的原样返回。
func TestUGDetailText(t *testing.T) {
	cases := map[string]string{
		"param is invalid": "上游要求活动/作品参数，插件无法自动获取",
		"参数缺失":             "上游要求活动/作品参数，插件无法自动获取",
		"活动暂不可用":           "活动暂不可用",
		"HTTP 500 服务繁忙":    "HTTP 500 服务繁忙",
	}
	for message, want := range cases {
		if got := ugDetailText(message); got != want {
			t.Errorf("ugDetailText(%q) = %q, 期望 %q", message, got, want)
		}
	}
	if got := (UGResult{Label: "作品抽奖", Status: UGUnavailable, Message: "param is invalid"}).Describe(); !strings.Contains(got, "上游要求活动/作品参数") {
		t.Errorf("Describe 未翻译缺参文案: %s", got)
	}
}
