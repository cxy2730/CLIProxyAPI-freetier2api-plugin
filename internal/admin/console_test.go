package admin

// 本文件锁定控制台页面的关键前端不变量（密钥解码、请求闸门、单页布局、折叠区）。
// 页面与断言同包，改动页面时改造这里，不要再散落到插件根层测试。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// unwrapRe 匹配「再剥一层信封」的写法：.result 后面不接 s（区分合法的 .results）。
var unwrapRe = regexp.MustCompile(`\.result\s*[;,)\n]`)

// TestConsolePageDecodesPanelKey 验证控制台页能从 CPA 管理面板的持久化快照里解出管理密钥。
//
// 本页与面板同源，因此可以复用面板已保存的密钥，避免用户重复输入。面板
// （CPAMC）把整份认证状态存在 localStorage 的 "cli-proxy-auth" 键下，值是可逆
// 的 XOR 混淆：
//
//	前缀 "enc::v2::" + base64( utf8(JSON) XOR key )   （CPA 8.0.48 起）
//	前缀 "enc::v1::" + base64( utf8(JSON) XOR key )   （旧版，仍须兼容）
//	key(v2) = utf8("cli-proxy-api-webui::secure-storage|<host>")
//	key(v1) = utf8("cli-proxy-api-webui::secure-storage|<host>|<userAgent>")
//	JSON    = {"state":{...,"managementKey":"<密钥>",...},"version":N}
//
// 面板 8.0.48 把派生密钥里的 userAgent 去掉并升到 v2 前缀（浏览器升级后
// 不会再解出乱码），读取端两种前缀都认。本页若只认 v1，用户明明勾了
// 「记住密码」也会看到「未检测到可用的管理密钥」。
//
// 密钥由公开值派生，所以同源页面能解出来。这里断言页面确实实现了这套算法
// （算法细节若与面板不一致，用户会看到 401 且原因极难定位）。
func TestConsolePageDecodesPanelKey(t *testing.T) {
	page := Page("/plugins/freetier")

	// 混淆格式与快照字段必须与面板一致。
	for _, literal := range []string{
		`var OBF_PREFIX_RE = /^enc::v(\d+)::/`,   // 版本前缀：门禁与解码同源
		`"cli-proxy-api-webui::secure-storage"`,  // 种子
		`var PANEL_STORE_KEY = "cli-proxy-auth"`, // 面板的存储键名
		`var PANEL_KEY_FIELD = "managementKey"`,  // 密钥在快照里的字段名
		`localStorage.getItem(PANEL_STORE_KEY)`,  // 只读面板快照
		"snapshot.state",                         // zustand persist 的 {state,version}
		"location.host",                          // 密钥派生输入
		"navigator.userAgent",                    // 密钥派生输入
		"atob(",                                  // base64 解码
		"TextDecoder",                            // utf8 解码
		"function deobfuscateAll(",               // 解码出口（官方口径在前）
		"function snapshotKey(",                  // 快照解析
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page is missing %s (key decoding would break)", literal)
		}
	}

	// 门禁与解码必须认同一个版本家族：只认 v1/v2 会让「面板又升级」变成静默
	// 失败（历史回归），而 normalizeKeyValue 也不该把未知版本的混淆串当密钥。
	if !strings.Contains(page, "return OBF_PREFIX_RE.test(raw);") {
		t.Fatal("isObfuscated must gate on OBF_PREFIX_RE (whole enc::vN:: family)")
	}

	// 旧版面板的键名不再读取：读到过期值会让页面带着错密钥去打探针，
	// 白白消耗 CPA 的失败次数配额。
	for _, legacy := range []string{
		`getItem("managementKey")`,
		"detectKey(",
		`localStorage.getItem("cpa-management-key")`,
	} {
		if strings.Contains(page, legacy) {
			t.Fatalf("console page must not read the legacy key source %s", legacy)
		}
	}

	// 密钥必须校验字符集：面板的混淆密钥由 userAgent 派生，浏览器升级后会
	// 解出乱码；把乱码放进 Authorization 头会被浏览器拒绝（Safari 报
	// "The string did not match the expected pattern."），且极难定位。
	if !strings.Contains(page, "KEY_RE") {
		t.Fatal("console page must validate the key charset before using it in a header")
	}

	// 自动复用失败时必须能退回手填，否则用户会面对一片空白。
	for _, literal := range []string{`id="keyBox"`, `id="keyInput"`, `id="btnUseKey"`, "revealKeyInput"} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page is missing the manual key fallback (%s)", literal)
		}
	}
}

// TestConsolePagePanelKeyRoundTrip 用官方面板算法生成快照，实际执行页面里的
// 解码函数，锁住「v1/v2 都能解出密钥、未知版本不解但留告警」。
//
// 上一条测试只断言字面量存在——算法改错（比如给 v2 掺 userAgent、或删掉 v1
// 分支）它一条都不会红。这里把页面里那段函数抽出来交给 node 跑，输入是测试
// 侧按面板文档算出来的密文，输出必须与预期逐字一致。
//
// 没有 node 时跳过：这是加倍的保险，不引入构建依赖。
func TestConsolePagePanelKeyRoundTrip(t *testing.T) {
	nodePath, errLook := exec.LookPath("node")
	if errLook != nil {
		t.Skip("node 不可用，跳过页面密钥算法的交叉验证")
	}
	page := Page("/plugins/freetier")
	start := strings.Index(page, "var PANEL_STORE_KEY")
	end := strings.Index(page, "// managementKey 返回当前生效的管理密钥。")
	if start < 0 || end <= start {
		t.Fatal("console page key-decoding block not found")
	}

	const (
		salt = "cli-proxy-api-webui::secure-storage"
		host = "cpa.example.com:8317"
		ua   = "Mozilla/5.0 fixture"
		key  = "sk-fixture-key"
	)
	// 载荷必须长于派生密钥：真实快照是 KB 级，短载荷会让「密钥是另一把的前缀」
	// 这一族错误（v1 = v2 密钥 + "|" + UA）在 XOR 下无法区分，测试就白测了。
	payload := `{"state":{"apiBase":"http://127.0.0.1:8317","managementKey":"` + key +
		`","note":"` + strings.Repeat("payload", 40) + `"},"version":0}`
	cases := []struct {
		name   string
		stored string
		want   string
		warns  bool
	}{
		{"v2 官方派生 seed|host", encodePanelSnapshot(2, payload, salt+"|"+host), key, false},
		{"v1 官方派生 seed|host|userAgent", encodePanelSnapshot(1, payload, salt+"|"+host+"|"+ua), key, false},
		{"v2 分支面板派生 seed|v2|host", encodePanelSnapshot(2, payload, salt+"|v2|"+host), key, false},
		{"未知版本 enc::v9::", encodePanelSnapshot(9, payload, salt+"|v9|"+host), "", true},
	}

	type caseJSON struct {
		Name   string `json:"name"`
		Stored string `json:"stored"`
		Want   string `json:"want"`
	}
	type resultJSON struct {
		Name   string `json:"name"`
		Want   string `json:"want"`
		Got    string `json:"got"`
		Err    string `json:"err"`
		Warned bool   `json:"warned"`
	}

	encodedCases, errMarshal := json.Marshal(func() []caseJSON {
		out := make([]caseJSON, 0, len(cases))
		for _, c := range cases {
			out = append(out, caseJSON{Name: c.name, Stored: c.stored, Want: c.want})
		}
		return out
	}())
	if errMarshal != nil {
		t.Fatalf("marshal cases: %v", errMarshal)
	}

	script := strings.NewReplacer(
		"__BLOCK__", strconv.Quote(page[start:end]),
		"__CASES__", string(encodedCases),
		"__HOST__", strconv.Quote(host),
		"__UA__", strconv.Quote(ua),
	).Replace(`const block = __BLOCK__;
const cases = __CASES__;
globalThis.location = { host: __HOST__ };
Object.defineProperty(globalThis, 'navigator', { value: { userAgent: __UA__ }, configurable: true });
const store = {};
globalThis.localStorage = {
  getItem: function (k) { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
  setItem: function (k, v) { store[k] = v; },
};
const warnings = [];
const page = new Function('console', block + ';return {panelAuthKey: panelAuthKey};')({
  warn: function () { warnings.push(Array.prototype.join.call(arguments, ' ')); },
  log: function () {},
});
const out = cases.map(function (c) {
  store['cli-proxy-auth'] = c.stored;
  warnings.length = 0;
  let got = '', err = '';
  try { got = page.panelAuthKey(); } catch (e) { err = String(e); }
  return { name: c.name, want: c.want, got: got, err: err, warned: warnings.length > 0 };
});
process.stdout.write(JSON.stringify(out));
`)

	scriptPath := filepath.Join(t.TempDir(), "panelkey.cjs")
	if errWrite := os.WriteFile(scriptPath, []byte(script), 0o600); errWrite != nil {
		t.Fatalf("write script: %v", errWrite)
	}
	output, errRun := exec.Command(nodePath, scriptPath).CombinedOutput()
	if errRun != nil {
		t.Fatalf("node 执行页面解码函数失败: %v\n%s", errRun, output)
	}
	var results []resultJSON
	if errUnmarshal := json.Unmarshal(output, &results); errUnmarshal != nil {
		t.Fatalf("解析 node 输出失败: %v（输出 %q）", errUnmarshal, output)
	}
	if len(results) != len(cases) {
		t.Fatalf("结果条数 = %d, want %d", len(results), len(cases))
	}
	for index, result := range results {
		if result.Err != "" {
			t.Fatalf("%s: 解码抛错: %s", result.Name, result.Err)
		}
		if result.Got != cases[index].want {
			t.Fatalf("%s: 解出 %q, want %q", result.Name, result.Got, cases[index].want)
		}
		if result.Warned != cases[index].warns {
			t.Fatalf("%s: 告警 = %v, want %v（未知版本必须留线索）", result.Name, result.Warned, cases[index].warns)
		}
	}
}

// encodePanelSnapshot 按面板文档把明文编成指定版本的混淆串。
func encodePanelSnapshot(version int, payload, keyText string) string {
	return fmt.Sprintf("enc::v%d::%s", version, encodePanelPayload(payload, keyText))
}

// encodePanelPayload 用面板同款 XOR + base64 编码载荷（测试侧的独立实现，
// 与页面实现互为对照，避免「同一处写错两边一起错」）。
func encodePanelPayload(payload, keyText string) string {
	key := []byte(keyText)
	raw := []byte(payload)
	for index := range raw {
		raw[index] ^= key[index%len(key)]
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// TestConsolePageNeverCallsWithoutKey 验证控制台页在身份确认之前不发任何管理请求。
//
// CPA 按 IP 统计管理密钥失败次数（**未携带密钥也算**），5 次即把整个
// /v0/management 封禁 30 分钟。早期版本在拿不到密钥时仍并发发 5 个首屏请求，
// 因此"打开插件页"会直接把后台锁掉。本测试把修复后的三条不变量锁住：
//
//  1. 没有密钥 → 一个请求都不发；
//  2. 有密钥 → 只发一个探针，成功才加载首屏，且首屏串行；
//  3. 任一 401/403 → 立刻停掉全部自动刷新与轮询定时器。
func TestConsolePageNeverCallsWithoutKey(t *testing.T) {
	page := Page("/plugins/freetier")

	// 1) api() 闸门：未认证/已失效不出网，没有密钥也不出网。
	for _, literal := range []string{
		"if (stopped) return Promise.resolve(null);",
		"if (!key) return Promise.resolve(null);",
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page is missing the api() guard %q", literal)
		}
	}

	// 2) 单探针先于首屏；首屏是串行链而不是并发铺开。
	if !regexp.MustCompile(`probeManagementAuth\(\)[\s\S]{0,800}startConsole\(\)`).MatchString(page) {
		t.Fatal("console page must validate the key with a single probe before loading data")
	}
	if !strings.Contains(page, `loadVendors().then(function () {`) {
		t.Fatal("console page must load the first screen serially (vendors first)")
	}

	// 3) 鉴权失败必须一次就停掉全部自动请求（登录轮询 + 页面轮询）。
	for _, literal := range []string{
		"function haltOnAuthFailure(status) {",
		"stopLoginPolling();",
		"stopped = true;",
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page is missing the auth-failure stop %q", literal)
		}
	}

	// 手输也必须先探针验证再记忆，不能先写 sessionStorage 再试；
	// 验证通过后要把 authReady 置真，否则日志轮询永远不会启动。
	if !regexp.MustCompile(`probeManagementAuth\(\)[\s\S]{0,900}setItem\(MANUAL_STORE_KEY`).MatchString(page) {
		t.Fatal("manually entered keys must be probe-verified before being remembered")
	}
	if !regexp.MustCompile(`setItem\(MANUAL_STORE_KEY, value\)[\s\S]{0,200}authReady = true`).MatchString(page) {
		t.Fatal("manual login must mark authReady after the probe succeeds")
	}
}

// TestConsolePageDoesNotUnwrapHostBody 验证页面不会误剥响应字段。
//
// 宿主把插件的响应体**原样**透传给浏览器：{ok, result} 那层信封是插件与宿主
// 之间的 RPC 格式，不会到达前端。若页面再剥一层 "result"，所有业务字段都会
// 变成 undefined——表现就是「接口 200 但界面全空」，且极难定位。
func TestConsolePageDoesNotUnwrapHostBody(t *testing.T) {
	page := Page("/plugins/freetier")

	// 注意用正则精确匹配：字符串包含 "data.result" 也会命中 "data.results"（合法字段）。
	if unwrapRe.MatchString(page) {
		t.Fatal("console page must not unwrap a second envelope layer (the host passes the body through verbatim)")
	}
	// 业务字段必须是从响应顶层直接取的。
	for _, field := range []string{
		"status.accounts", // 账号列表
		"data.models",     // 模型清单
		"data.results",    // 额度/任务结果
	} {
		if !strings.Contains(page, field) {
			t.Fatalf("console page must read %q directly from the response body", field)
		}
	}
}

// TestConsolePageWiresHostOAuthLogin 验证「添加账号」接的是宿主的 OAuth 端点。
//
// 宿主为插件提供两段式登录：
//
//	GET /v0/management/<provider>-auth-url?realm=...   → 转给 auth.login.start
//	GET /v0/management/get-auth-status?state=...       → 转给 auth.login.poll
//
// 页面必须走这两个端点（而不是插件自己的路由），否则「添加账号」不可用。
func TestConsolePageWiresHostOAuthLogin(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, literal := range []string{
		"/freetier-auth-url", // 与 providerKey 拼接，宿主按此匹配 auth provider
		"get-auth-status",    // 宿主提供的状态轮询端点
		`id="addVendorMenu"`, // 供应商下拉菜单（替代原先的两个固定按钮）
		`id="btnAddAccount"`, // 添加账号入口
		"startLogin",         // 登录流程
		"pollLogin",          // 轮询流程
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page is missing the add-account wiring (%s)", literal)
		}
	}
	// 这两个接口在**宿主根路径**下（/v0/management/<provider>-auth-url），
	// 不能带插件前缀：宿主按路径提取 provider，带前缀会得到
	// "plugins/freetier/workbuddy"，校验失败直接 404。
	if !strings.Contains(page, "hostPath(") {
		t.Fatal("host-level OAuth endpoints must not be prefixed with the plugin path")
	}
	if strings.Contains(page, `api("GET", "/freetier-auth-url`) {
		t.Fatal("auth-url must go through hostPath() (the host extracts the provider from the path)")
	}
	if !strings.Contains(page, `var HOST_MGMT = "/v0/management"`) {
		t.Fatal("console page must know the host management prefix")
	}

	// 供应商必须可选：页面从 /vendors 拉清单填进下拉，且登录时把 vendor
	// 作为参数传给宿主（宿主只认路径里的 provider，选哪家由插件按参数决定）。
	if !strings.Contains(page, "loadVendors") {
		t.Fatal("console page must load the vendor list from the backend")
	}
	if !strings.Contains(page, "vendor=") {
		t.Fatal("console page must pass the selected vendor to the host login endpoint")
	}
}

// TestConsolePageIsSingleScrollLayout 验证页面是单页滚动而非标签页。
//
// 标签页会把额度、模型、任务藏起来，管理页应当一眼看全。
func TestConsolePageIsSingleScrollLayout(t *testing.T) {
	page := Page("/plugins/freetier")

	// 不应再有导航与视图切换。
	for _, forbidden := range []string{"<nav>", "data-view=", "switchView", `id="view-accounts" hidden`} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("console page must be a single scrolling page, found %q", forbidden)
		}
	}
	// 各区块都必须在页面上直接可见（不带 hidden）。
	for _, section := range []string{"账号", "额度", "模型", "设置"} {
		if !strings.Contains(page, section) {
			t.Fatalf("console page is missing the %q section", section)
		}
	}
	// 额度与模型必须有自己的容器与按钮。
	for _, literal := range []string{
		`id="btnLoadQuotas"`, // 额度：按钮 + 并入账号表的列
		`id="models"`, `id="btnLoadModels"`, `id="modelFilter"`,
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page is missing %s", literal)
		}
	}
	// 任务执行进度不再单独一张表：并入各账号的任务清单。
	if strings.Contains(page, `id="queue"`) {
		t.Fatal("queue progress must be merged into the per-account task lists")
	}

	// 额度不再单独占一张表：结果并入账号表的「剩余 / 总额」与「剩余率」两列。
	if strings.Contains(page, `id="quotas"`) {
		t.Fatal("quota must be merged into the accounts table, not a separate section")
	}
	if !strings.Contains(page, "剩余 / 总额") || !strings.Contains(page, "剩余率") {
		t.Fatal("accounts table must carry the quota columns")
	}
}

// TestConsolePageHasBulkTaskButton 验证「一键完成」是批量按钮而非每账号一个。
//
// 逐账号一个按钮在账号多时既难用也容易误点；统一成「全部账号依次执行」。

// TestConsolePageStopsPollingOnAuthFailure 验证鉴权失败会停止轮询。
//
// CPA 按客户端 IP 统计管理鉴权失败次数，连续失败会封禁该 IP
// （连正确密钥也会被拒），因此必须停。
func TestConsolePageStopsPollingOnAuthFailure(t *testing.T) {
	page := Page("/plugins/freetier")
	if !strings.Contains(page, "haltOnAuthFailure") {
		t.Fatal("console page must stop polling on auth failure to avoid an IP ban")
	}
	if !strings.Contains(page, "401") || !strings.Contains(page, "403") {
		t.Fatal("console page must treat both 401 and 403 as auth failures")
	}
}

// TestConsolePageIsStaticAndSelfContained 验证页面不含任何凭证数据。
//
// 资源页不做管理鉴权，因此页面本身绝不能带账号或令牌。
func TestConsolePageIsStaticAndSelfContained(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, forbidden := range []string{"accessToken", "refreshToken", "device_token", "Authorization: Bearer sk-"} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("console page must not embed credential material (%q found)", forbidden)
		}
	}
	// 唯一的网络目标必须是 CPA 自身。
	if strings.Contains(page, "http://") && !strings.Contains(page, "http://127.0.0.1") {
		t.Fatal("console page must not reference external origins")
	}
	// 页面必须能停止自动刷新（避免触发 CPA 的按 IP 鉴权封禁）。
	if !strings.Contains(page, "haltOnAuthFailure") {
		t.Fatal("console page must stop polling on auth failure")
	}
}

// TestSettingsKeepsOnlyTwoToggles 验证运行期设置只剩两个开关。
//
// 2026-11-02 用户要求：运行期设置只保留「每日自动签到」与「签到后自动跑
// 后续任务（开学季 / 连登奖励）」。执行时间与任务排程不再暴露给用户——
// 时间沿用已存值，排程沿用默认值，保存时不提交这两个字段，后端对缺省
// 字段不做改动，因此不会清掉既有配置。
func TestSettingsKeepsOnlyTwoToggles(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, marker := range []string{
		`id="setAutoCheckin"`,
		`id="setAutoTasks"`,
		"每日自动签到",
		"签到后自动跑后续任务（开学季 / 连登奖励）",
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("运行期设置缺少 %q", marker)
		}
	}
	for _, gone := range []string{
		`id="setCheckinAt"`,
		`id="taskSchedule"`,
		`id="scheduleFold"`,
		"任务排程",
		"执行时间",
	} {
		if strings.Contains(page, gone) {
			t.Fatalf("运行期设置不应再出现 %q", gone)
		}
	}
	// 保存只提交两个开关。
	if strings.Contains(page, "task_schedules") {
		t.Error("保存设置不应再提交 task_schedules（会让后端按空表覆盖排程）")
	}
	if strings.Contains(page, `getElementById("setCheckinAt")`) {
		t.Error("保存设置不应再读执行时间输入框")
	}
}

// TestVendorMenuShowsAllVendors 验证「添加账号」下拉能完整看到全部渠道。
//
// 供应商有 16 个，菜单原来既没有 max-height 也没有滚动，绝对定位溢出卡片
// 的部分会被后续 .card（带 backdrop-filter，自成层叠上下文）盖住，尾部
// 渠道永远看不到也滚不到。这里锁住三件事：限高 + 纵向滚动、展开时抬升
// 所在卡片、两个下拉都走同一个抬升函数。
func TestVendorMenuShowsAllVendors(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, marker := range []string{
		"min-width: 210px; max-height: min(72vh, 560px)",
		"overflow: hidden auto",
		"background: var(--pop)",
		"overscroll-behavior: contain",
		".card.menu-open { position: relative; z-index: 30; }",
		"function liftMenuCard(",
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("渠道下拉缺少 %q", marker)
		}
	}
	// 两个下拉（添加账号 / 模型列）都要抬升所在卡片，否则跨卡片溢出仍被遮。
	if got := strings.Count(page, "liftMenuCard(menu, "); got < 4 {
		t.Fatalf("下拉展开/收起都应调用 liftMenuCard，当前只调用了 %d 处", got)
	}
}

// TestConsolePageWiresLoginCallbackPasteBox 验证控制台把「粘贴回调链接」接到了插件
// 自己的管理路由上。
//
// 远程 / Docker 部署时上游只认 localhost 回调，浏览器到不了插件的监听；页面若
// 没有这条兜底通道，Trae / CodeArts 的登录就永远停在「等待授权中…」。这里同时
// 锁住三件事：折叠块存在、提交按钮存在、请求打的是插件自有路由（不能是宿主路由
// ——宿主没有这个端点，打过去只会 404）。
func TestConsolePageWiresLoginCallbackPasteBox(t *testing.T) {
	page := Page("/plugins/freetier")
	for _, marker := range []string{
		`id="loginPasteBox"`,
		`id="loginPasteUrl"`,
		`id="btnSubmitCallback"`,
		`api("POST", "/login/callback"`,
		"paste_callback",
		"callback_hint",
	} {
		if !strings.Contains(page, marker) {
			t.Fatalf("console page missing %q", marker)
		}
	}
}

// TestConsoleQuotaCellUsesCapability 锁定账号表额度列的状态判定。
//
// 「查询中…」只能表示**确实在查**。上游没有额度接口的供应商（OpenCode ZEN）
// 永远不会有结果，若页面把「没有结果」一律当成查询中，那一行就会永久卡在
// 「查询中…」，而正确展示是「-- / --」。因此分支必须读后端给的能力标志
// supports_quota，而不是靠结果是否存在反推。
func TestConsoleQuotaCellUsesCapability(t *testing.T) {
	page := Page("/plugins/freetier")

	if !strings.Contains(page, "account.supports_quota") {
		t.Fatal("quota cell must branch on account.supports_quota")
	}
	// 页面不得再维护自己的额度缓存：它是后端缓存的第二份副本，键还靠猜测。
	if strings.Contains(page, "quotaByAuth") {
		t.Fatal("console must not keep a second quota cache (quotaByAuth); /status is the source of truth")
	}
	// 「查询中…」必须出现在 supports_quota 为真的分支里。
	if !regexp.MustCompile(`supports_quota[\s\S]{0,400}查询中`).MatchString(page) {
		t.Fatal("「查询中…」must be shown only when the vendor supports quota")
	}
}

// TestConsoleModelChangesTriggerHostReload 锁定「模型变更自动生效」的接线。
//
// 宿主只在 plugin.register / plugin.reconfigure 时重建模型注册表，而插件自己的
// /models/toggle 与 /models/alias 不会通知宿主。宿主也没有 reload 端点，唯一可用
// 的是写插件配置触发的隐含重载：PATCH /v0/management/plugins/:id/config。
//
// 该重载按配置文件 hash 判断是否需要执行，内容不变会被跳过，所以必须写入一个
// 会变化的键（config_reloaded_at）。页面不得再显示「重启 CPA 生效」的提示条，
// 也不得提供手动重载按钮——变更是自动同步的。
func TestConsoleModelChangesTriggerHostReload(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, literal := range []string{
		"syncModelConfig",                      // 同步入口
		"config_reloaded_at",                   // 制造配置内容变化的键
		`hostPath("/plugins/freetier/config")`, // 宿主插件配置路由
		`api("PATCH", configPath`,              // 浅合并写配置，触发宿主重载
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page missing model-sync wiring (%s)", literal)
		}
	}

	// 变更成功后必须同步，而不是留给用户手动操作。
	for _, hook := range []string{"syncModelConfig();"} {
		if strings.Count(page, hook) < 2 {
			t.Fatalf("console must call %s from both model toggle and alias paths", hook)
		}
	}

	// 旧的「待生效提示条 / 重启」说法必须彻底消失。
	for _, stale := range []string{"pendingBar", "setPendingBar", "需重启CPA生效", "应用变更"} {
		if strings.Contains(page, stale) {
			t.Fatalf("console must not keep the restart-pending UI anymore (%s)", stale)
		}
	}
}

// TestConsoleLoadModelsButtonRefreshesUpstream 锁定「读取模型清单」按钮的真实行为。
//
// 事故：该按钮曾绑定 loadModels（只重读插件内存缓存）。缓存一旦为空（插件刚
// 重载、探测失败），点它永远没反应，用户也就无从恢复整家供应商的模型列表。
// 按钮必须 POST /models/refresh 去上游拉一次，失败时把上游错误显示出来——
// 401 与超时的处理方式完全不同，藏进日志等于让用户自己猜。
func TestConsoleLoadModelsButtonRefreshesUpstream(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, literal := range []string{
		`api("POST", "/models/refresh"`, // 真正触发上游拉取
		`refreshModels(false);`,         // 按钮绑定到刷新入口
		"从上游刷新失败：",                      // 上游错误原样呈现
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page missing model-refresh wiring (%s)", literal)
		}
	}
	if strings.Contains(page, `addEventListener("click", loadModels`) {
		t.Fatal("the load-models button must not just re-read the local cache")
	}
}

// TestConsoleModelTableMergesIDAndCredits 锁定模型表的列重构：
// 「模型 ID」与「别名」合并为一列，原「别名」位置改为「费用倍率」。
func TestConsoleModelTableMergesIDAndCredits(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, literal := range []string{
		"MODEL_COLUMNS",    // 表头与单元格同源的列描述表
		"modelIDCell",      // 合并列（生效 ID + ✎）
		"modelCreditsCell", // 费用倍率列
		"model.credits",    // 读后端下发的 credits
		`"模型 ID"`,
		`"费用倍率"`,
		`"✎"`,              // 铅笔图标（字面量，不引入 SVG/innerHTML）
		"creditsSortOrder", // 倍率列点击排序
		"modelCreditValue", // 倍率值解析（无倍率沉底）
		"th.sortable",      // 表头可排序样式
		"sort-arrows",      // 双三角排序指示器容器
		"sort-arrow asc",   // 向上三角
		"sort-arrow desc",  // 向下三角
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page missing model-table marker %q", literal)
		}
	}

	// 旧的独立「别名」表头必须消失：它现在是模型 ID 列的一部分。
	if strings.Contains(page, `"名称", "别名"`) {
		t.Fatal("alias column must be merged into the model-id column")
	}

	// 编辑仍走原接口与同步入口（回归保护）。
	for _, literal := range []string{
		`api("POST", "/models/alias"`,
		"syncModelConfig();",
		"scope_id",
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("alias editing wiring lost: %s", literal)
		}
	}
}

// TestConsoleModelOptionalColumnsHiddenByDefault 锁定「输出上限/推理档位/能力」
// 可切换且默认显隐符合规范，偏好按浏览器持久化。
func TestConsoleModelOptionalColumnsHiddenByDefault(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, literal := range []string{
		`id="btnModelCols"`,
		`id="modelColsMenu"`,
		"cols-setter",
		`"freetier-model-optional-columns"`,
		"localStorage.getItem(MODEL_COLUMNS_STORE_KEY)",
		"localStorage.setItem(MODEL_COLUMNS_STORE_KEY",
		"function readColumnPrefs()",
		"function visibleColumns()",
		"function toggleOptionalColumn(",
		"列展示 ▾",
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page missing column-visibility wiring %q", literal)
		}
	}

	// 共有三列可切换：输出上限、推理档位、能力——其余列必须恒显。
	if got := strings.Count(page, "optional: true"); got != 3 {
		t.Fatalf("optional columns = %d, want 3 (输出上限/推理档位/能力)", got)
	}
	for _, key := range []string{"output", "efforts", "caps"} {
		pattern := regexp.MustCompile(`key: "` + key + `"[\s\S]{0,120}optional: true`)
		if !pattern.MatchString(page) {
			t.Fatalf("column %q must be optional", key)
		}
	}

	// 输出上限、推理档位默认隐藏，能力默认展示
	for _, key := range []string{"output", "efforts"} {
		pattern := regexp.MustCompile(`key: "` + key + `"[\s\S]{0,120}defaultShow: false`)
		if !pattern.MatchString(page) {
			t.Fatalf("column %q must have defaultShow: false", key)
		}
	}
	if !regexp.MustCompile(`key: "caps"[\s\S]{0,120}defaultShow: true`).MatchString(page) {
		t.Fatal(`column "caps" must have defaultShow: true`)
	}

	// 缺省/损坏/存储不可用时必须回到空对象。
	if !regexp.MustCompile(`catch \(e\) \{\s*return \{\};`).MatchString(page) {
		t.Fatal("column preferences must default to an empty object")
	}
}

func TestConsolePageSettingsLabels(t *testing.T) {
	page := Page("/plugins/freetier")
	for _, required := range []string{
		"每日自动签到",
		// 开关只影响签到后的两个后续任务，标签必须写清范围，不能叫「每日自动任务」。
		"签到后自动跑后续任务（开学季 / 连登奖励）",
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("console page missing %q", required)
		}
	}
	for _, stale := range []string{"自动每日任务", "> 每日自动任务<"} {
		if strings.Contains(page, stale) {
			t.Fatalf("console page must not label the follow-up switch as 每日自动任务: %q", stale)
		}
	}
}

// TestConsoleSettingsSyncsModelRegistryImmediately 锁定渠道标识保存后立即生效：
// 响应带 model_registry_dirty 时复用 syncModelConfig 触发宿主重载，页面不再
// 让用户「等模型清单刷新（或重启宿主）」。

// TestConsoleAccountAliasReusesInlineEditor 锁定账号别名的接线与「只有一份」。
//
// 账号别名与模型别名是同一套交互（点击 ✎、回车/失焦保存、Esc 取消），实现
// 必须只有 editableCell 一份：复制第二份意味着键盘行为、回退策略、错误提示
// 迟早分叉。账号别名按账号身份 file_id 提交，不用昵称/邮箱。
func TestConsoleAccountAliasReusesInlineEditor(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, literal := range []string{
		"function editableCell(opts)", // 共用编辑器
		"function accountNameCell(account)",
		"accountNameCell(account)", // 账号列确实用它渲染
		`api("POST", "/accounts/alias"`,
		"account.file_id",
		"account.alias",
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page missing account-alias wiring %q", literal)
		}
	}

	if count := strings.Count(page, `input.addEventListener("blur"`); count != 1 {
		t.Fatalf("inline editing must have exactly one implementation, found %d blur handlers", count)
	}
}

// TestConsoleAccountAliasPrefillsCurrentName 锁定账号别名的编辑预填与归一：
// 点开编辑框时预填当前生效名称（与模型别名一致），留空或填回原名称都视为
// 「不设别名」，不会在 state 里留一条与上游同名的副本。
func TestConsoleAccountAliasPrefillsCurrentName(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, literal := range []string{
		"function accountOriginalName(account)",
		"prefill: function () { return accountOriginalName(account); }",
		`normalize: function (next) { return next === accountOriginalName(account) ? "" : next; }`,
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page missing account-alias prefill wiring %q", literal)
		}
	}
}

// TestConsoleAccountsTableSupportsBatchSelection 锁定账号表的批量操作接线：
// 勾选列在首位、状态列在供应商列左侧、操作列已移除（删除改到顶部按钮）。
func TestConsoleAccountsTableSupportsBatchSelection(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, literal := range []string{
		`["", "状态", "供应商", "账号", "签到", "剩余 / 总额", "剩余率", "刷新时间"]`,
		"function accountCheckCell(account)",
		`id="btnDeleteAccounts"`,
		`id="btnDisableAccounts"`,
		`id="btnEnableAccounts"`,
		`id="btnSelectAllAccounts"`,
		`id="btnSelectNoneAccounts"`,
		`"/accounts/enabled"`,
		`id="accountFilter"`,
		"function accountMatchesFilter(account, filter)",
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page missing account batch wiring %q", literal)
		}
	}
	if strings.Contains(page, `"操作"`) {
		t.Fatal("账号表的操作列已移除，删除改由顶部按钮完成")
	}
}

// TestConsolePageShowsCheckinHistory 验证签到历史在控制台可展开查看。
//
// 横幅只显示几秒钟，失败账号与报错原文必须能在页面上事后回溯；历史入口挂在
// 每个账号自己的签到列上（同一张表、同一份 /status 数据，不再发额外请求）。
func TestConsolePageShowsCheckinHistory(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, literal := range []string{
		"account.checkin_history",      // 历史来自 /status 的账号条目
		"buildCheckinHistoryRow",       // 展开行的构造点
		`entry.trigger === "schedule"`, // 触发方式要在页面上区分定时/手动
		"checkinStatusPill",            // 三种状态各自着色
	} {
		if !strings.Contains(page, literal) {
			t.Fatalf("console page is missing the checkin history UI (%s)", literal)
		}
	}
}

// 福利领取合并在既有签到入口，页面不应按供应商硬编码新按钮。
func TestConsoleBenefitsUseExistingCheckinEntry(t *testing.T) {
	page := Page("/plugins/freetier")
	if strings.Contains(page, `account.vendor_id === "codearts"`) || strings.Contains(page, `"领取每日福利"`) {
		t.Fatal("benefits must use the shared check-in flow")
	}
	for _, want := range []string{`runCheckin(selectedAccountIDs(), this)`, `api("POST", "/checkin", { account_ids: accountIDs })`, `r.message`, `loadQuotas(true, accountIDs)`} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing shared check-in wiring: %s", want)
		}
	}
}

// TestConsoleScanTableScrollsInsideWrapper 验证扫描表的横向滚动发生在表格自己的容器里。
//
// 扫描表有 5 列、窄屏必然超宽。如果让 .card 承担 overflow-x，滚动会连卡片标题一起
// 横向卷走（320px 时标题左移 157px），看起来像标题被裁掉。滚动容器必须是只包表格的
// .table-scroll，卡片与标题保持不动。

// TestConsoleAccountAndModelStatusLabels 锁定控制台账号与模型列表的状态与操作文案及排序：
// 正常 -> 启用，禁用/已禁用 -> 停用，批量按钮为「停用选中」，移除旧提示，且账号列表按「启用在上停用在下」排序。
func TestConsoleAccountAndModelStatusLabels(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, want := range []string{
		`<button class="act" id="btnDisableAccounts">停用选中</button>`,
		`<button class="act" id="btnDisableModels">停用选中</button>`,
		`account.disabled ? pill("停用", "err") : pill("启用", "ok")`,
		`model.disabled ? pill("停用", "err") : pill("启用", "ok")`,
		`// 排序：状态 + 供应商名称 + 账号 ID（启用在上，停用在下；同状态内按 供应商名称 + 账号 ID 升序）`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("console page missing expected label: %s", want)
		}
	}

	for _, forbidden := range []string{
		`<button class="act" id="btnDisableAccounts">禁用选中</button>`,
		`<button class="act" id="btnDisableModels">禁用选中</button>`,
		`pill("已禁用", "err")`,
		`pill("禁用", "err")`,
		`勾选账号后可批量禁用`,
	} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("console page contains forbidden deprecated label: %s", forbidden)
		}
	}
}

// TestConsoleAutoAllReportsOutcomeHonestly 锁定「一键做任务」的收尾汇报。
//
// 旧实现无论结果如何都弹绿色横幅、5 秒后消失，8 个账号里 6 个根本没执行
// 也报「全部账号执行完毕」；「完成 0」既可能是渠道不支持也可能是卡住，
// 读者无从分辨。这里要求：按 done/skip/error 分级着色（中断不落绿）、
// 失败与跳过带上游原因、明细多时常驻、横幅限高可滚动。

// TestConsoleAutoAllCoversEveryAccount 锁定「一键任务」的语义：
// 未勾选时提交全部渠道的全部账号，进度用快照轮询，结果只按上游 message 汇总。
func TestConsoleAutoAllCoversEveryAccount(t *testing.T) {
	page := Page("/plugins/freetier")

	for _, want := range []string{
		`<button class="act" id="btnAutoAll">一键任务</button>`,
		`runAutoAll(selectedAccountIDs(), this)`,
		`api("POST", "/tasks/autoall", { account_ids: accountIDs })`,
		`api("GET", "/tasks/autoall/status")`,
		`err && err.status === 409`, // 已有作业在跑时接着看进度，不报错
		`row.skipped`,               // 账号级「未执行」要单独说明
		`it.status === "error"`,     // 失败按上游 message 报
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("console page is missing the one-click task wiring (%s)", want)
		}
	}

	// 不允许在前端臆测结论：领没领到由上游 message 决定。
	for _, bad := range []string{"已全部领过", "任务已全部完成"} {
		if strings.Contains(page, bad) {
			t.Fatalf("one-click task must not assert %q", bad)
		}
	}

	// 被中断时上游 message 优先于逐账号明细，且不能落绿。
	if !strings.Contains(page, `if (result.cancelled) { banner("warn", "一键任务被中断：`) {
		t.Fatal("cancelled 必须先把上游 message 摆出来，并落 warn")
	}
	// 局部变量 head 会遮蔽 document.head，按 #75 复审要求改名。
	if strings.Contains(page, "var head = rows.length") {
		t.Fatal("summaryHead 不能再叫 head")
	}
}
