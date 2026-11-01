package tests

// 本文件覆盖 response.normalize_before（插件注册的全局响应归一化钩子）。
//
// 宿主对每一条 SSE 行都调一次该钩子，RPC 参数里带着完整客户端请求与完整
// 上游请求（可达数 MB，base64 编码）。issue #9 的 CPU 热点就来自这里：
// 整包反序列化会把两份大请求体全部解码，而插件只用 Stream / FromFormat /
// ToFormat / Body。因此本文件的重点不只是「跨协议要补 data: 前缀」，
// 还有「开销不随请求体大小增长」。
//
// 背景：补 data: 前缀是 issue #5 / #8 的修复（Claude 转换器要求该前缀，
// 缺了会整帧丢弃），不能因为性能优化退回去。

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"freetier2api-plugin/cpasdk/pluginabi"
	"freetier2api-plugin/cpasdk/pluginapi"
	"freetier2api-plugin/internal/app"
)

// callNormalizeBefore 调用 response.normalize_before 并返回插件给出的 Body。
func callNormalizeBefore(t *testing.T, request pluginapi.ResponseTransformRequest) []byte {
	t.Helper()
	result := callMethod(t, pluginabi.MethodResponseNormalizeBefore, request)
	var response pluginapi.PayloadResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		t.Fatalf("decode normalize response: %v (raw=%s)", errUnmarshal, result)
	}
	return response.Body
}

// TestResponseNormalizeBeforeFramesCrossProtocol 验证跨协议流式分片被补上 SSE 信封。
//
// 三种目标格式都走一遍：Claude 转换器硬性要求 data: 前缀，Gemini 与
// openai-responses 转换器容忍它——统一补前缀对三者都正确。
func TestResponseNormalizeBeforeFramesCrossProtocol(t *testing.T) {
	setupTestPlugin(t)

	for _, toFormat := range []string{"claude", "gemini", "openai-response"} {
		t.Run(toFormat, func(t *testing.T) {
			body := callNormalizeBefore(t, pluginapi.ResponseTransformRequest{
				FromFormat: "openai",
				ToFormat:   toFormat,
				Model:      "glm-5.2",
				Stream:     true,
				Body:       []byte(`{"id":"c1","choices":[]}`),
			})
			want := "data: {\"id\":\"c1\",\"choices\":[]}\n\n"
			if string(body) != want {
				t.Fatalf("body = %q, want %q", body, want)
			}
		})
	}
}

// TestResponseNormalizeBeforePassesThrough 验证不需要归一化的调用返回空 Body。
//
// 空 Body 在宿主侧是明确的 no-op（保留原分片），因此这里既省一次克隆，
// 也避免同格式路径出现 data: data: 双层前缀。
func TestResponseNormalizeBeforePassesThrough(t *testing.T) {
	setupTestPlugin(t)

	cases := []struct {
		name    string
		request pluginapi.ResponseTransformRequest
	}{
		{
			name: "同格式 openai 不做任何处理",
			request: pluginapi.ResponseTransformRequest{
				FromFormat: "openai", ToFormat: "openai", Stream: true,
				Body: []byte(`{"id":"c1"}`),
			},
		},
		{
			name: "非流式请求不需要信封",
			request: pluginapi.ResponseTransformRequest{
				FromFormat: "openai", ToFormat: "claude", Stream: false,
				Body: []byte(`{"id":"c1"}`),
			},
		},
		{
			name: "源格式不是 chat-completions",
			request: pluginapi.ResponseTransformRequest{
				FromFormat: "gemini", ToFormat: "claude", Stream: true,
				Body: []byte(`{"id":"c1"}`),
			},
		},
		{
			name: "目标格式为空",
			request: pluginapi.ResponseTransformRequest{
				FromFormat: "openai", ToFormat: "", Stream: true,
				Body: []byte(`{"id":"c1"}`),
			},
		},
		{
			name: "已带 data: 前缀的分片不再包装",
			request: pluginapi.ResponseTransformRequest{
				FromFormat: "openai", ToFormat: "claude", Stream: true,
				Body: []byte("data: {\"id\":\"c1\"}"),
			},
		},
		{
			name: "[DONE] 不是合法 JSON 载荷",
			request: pluginapi.ResponseTransformRequest{
				FromFormat: "openai", ToFormat: "claude", Stream: true,
				Body: []byte("[DONE]"),
			},
		},
		{
			name: "空 Body",
			request: pluginapi.ResponseTransformRequest{
				FromFormat: "openai", ToFormat: "claude", Stream: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if body := callNormalizeBefore(t, tc.request); len(body) != 0 {
				t.Fatalf("body = %q, want empty (no-op)", body)
			}
		})
	}
}

// TestResponseNormalizeBeforeToleratesUnknownFields 验证宿主新增字段不影响判定。
//
// 解析器只认顶层对象里的已知键，其余键（含未来宿主可能加的包装字段）
// 一律跳过，而不是据此判定「不需要处理」。
func TestResponseNormalizeBeforeToleratesUnknownFields(t *testing.T) {
	setupTestPlugin(t)

	raw := []byte(`{"SchemaVersion":6,"HostCallbackID":"cb-1","FromFormat":"openai",` +
		`"Metadata":{"nested":[1,2,{"deep":"a\"b"}]},"ToFormat":"claude",` +
		`"Model":"glm-5.2","Stream":true,"OriginalRequest":"YWJj","Body":"eyJpZCI6ImMxIn0="}`)

	result := callMethod(t, pluginabi.MethodResponseNormalizeBefore, json.RawMessage(raw))
	var response pluginapi.PayloadResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		t.Fatalf("decode normalize response: %v", errUnmarshal)
	}
	if got, want := string(response.Body), "data: {\"id\":\"c1\"}\n\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

// TestResponseNormalizeBeforeIgnoresLargeRequestBodies 是 issue #9 的回归护栏。
//
// 宿主逐分片带上完整客户端请求与上游请求。这两份字段无论多大，都不该
// 出现在插件的解码路径上：分配量必须与它们的大小无关。
func TestResponseNormalizeBeforeIgnoresLargeRequestBodies(t *testing.T) {
	setupTestPlugin(t)

	request := func(extraBytes int) pluginapi.ResponseTransformRequest {
		return pluginapi.ResponseTransformRequest{
			FromFormat:        "openai",
			ToFormat:          "claude",
			Model:             "glm-5.2",
			Stream:            true,
			OriginalRequest:   make([]byte, extraBytes),
			TranslatedRequest: make([]byte, extraBytes),
			Body:              []byte(`{"id":"c1","choices":[]}`),
		}
	}

	marshal := func(size int) []byte {
		raw, errMarshal := json.Marshal(request(size))
		if errMarshal != nil {
			t.Fatalf("marshal request: %v", errMarshal)
		}
		return raw
	}
	small := marshal(1 << 10) // 1 KiB
	large := marshal(4 << 20) // 4 MiB
	if len(small) >= len(large) {
		t.Fatalf("large request must be bigger: small=%d large=%d", len(small), len(large))
	}

	// 两份请求体都是零字节，base64 后是重复的 "AAAA…"：命中扫描器的
	// 字符串跳过路径，而不会被误当成字段值。
	//
	// 这里量的是**每次调用的分配字节**，不是分配次数：一次
	// `make([]byte, len(body))` 只算 1 次分配，次数断言根本看不见它；而
	// -race 的固定开销在字节尺度上可以忽略，字节断言反而更稳、更严。
	allocated := func(raw []byte) float64 {
		return allocatedBytesPerRun(t, 20, func() {
			if _, errHandle := testApp.HandleMethod(pluginabi.MethodResponseNormalizeBefore, raw); errHandle != nil {
				t.Fatalf("normalize: %v", errHandle)
			}
		})
	}
	smallBytes, largeBytes := allocated(small), allocated(large)
	// 容差取 8 KiB：常数开销（JSON 解码缓冲、信封拼接）只有几百字节，而任何
	// 按请求体大小走的分支都会在 4 MiB 这一侧至少多出兆级分配（光 base64 解码
	// 就是 3 MiB，再加一份副本）。64 KiB 的余量对 4 MiB 体量太宽——真有按体积
	// 走的小块分配时抓不住（#71 第二轮复审 P2-10）。
	const slack = 8 << 10
	if growth := largeBytes - smallBytes; growth > slack {
		t.Fatalf("每次调用的分配字节随请求体增长：1KiB=%.0f 4MiB=%.0f（+%.0f 字节，上限 %d）", smallBytes, largeBytes, growth, slack)
	}

	// 结果本身仍然正确：大请求体不影响信封补全。
	result := callMethod(t, pluginabi.MethodResponseNormalizeBefore, json.RawMessage(large))
	var response pluginapi.PayloadResponse
	if errUnmarshal := json.Unmarshal(result, &response); errUnmarshal != nil {
		t.Fatalf("decode normalize response: %v", errUnmarshal)
	}
	if got, want := string(response.Body), "data: {\"id\":\"c1\",\"choices\":[]}\n\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

// TestResponseNormalizeBeforeCompatWithExecutorChunks 串起 executor 出口与
// 跨协议转换两段：executor 投递裸 JSON（#5/#8 修复），normalize 再补一层
// data: 前缀——最终只出现一层前缀。
func TestResponseNormalizeBeforeCompatWithExecutorChunks(t *testing.T) {
	host := installFakeHost(t)
	setupTestPlugin(t)
	host.addAccount("1", "cn-account", sampleCredentialJSON("cn", "cn1"))
	host.setUpstream(func(method, url, body string) fakeUpstreamResponse {
		if strings.Contains(url, "/v2/chat/completions") {
			return fakeUpstreamResponse{Status: 200, Body: "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"A\"}}]}\n\ndata: [DONE]\n\n"}
		}
		if strings.Contains(url, "/activity/growth/heatmap") {
			return fakeUpstreamResponse{Status: 200, Body: `{"code":0,"data":{"cells":[]}}`}
		}
		if strings.Contains(url, "/v2/report") {
			return fakeUpstreamResponse{Status: 200, Body: `{"code":0,"data":{}}`}
		}
		return fakeUpstreamResponse{Status: 404, Body: `{"code":404}`}
	})

	callMethod(t, pluginabi.MethodExecutorExecuteStream, app.ExecutorRPCRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			AuthID:  "1",
			Model:   "cn:glm-5.2",
			Format:  app.FormatChatCompletions,
			Payload: []byte(`{"model":"cn:glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
		},
		StreamID: "s1",
	})
	if !waitFor(t, 3e9, func() bool { return len(host.emittedChunks()) > 0 }) {
		t.Fatal("executor emitted no chunks")
	}

	for _, chunk := range host.emittedChunks() {
		body := callNormalizeBefore(t, pluginapi.ResponseTransformRequest{
			FromFormat: "openai", ToFormat: "claude", Stream: true, Body: []byte(chunk),
		})
		if strings.HasPrefix(chunk, "data:") {
			t.Fatalf("executor chunk must be bare JSON, got %q", chunk)
		}
		if !strings.HasPrefix(string(body), "data: ") || !strings.HasSuffix(string(body), "\n\n") {
			t.Fatalf("normalized chunk must carry exactly one data: envelope, got %q", body)
		}
		if strings.HasPrefix(string(body), "data: data:") {
			t.Fatalf("double data: prefix regressed (#5/#8): %q", body)
		}
	}
}

// allocatedBytesPerRun 返回 fn 每次调用平均分配的字节数。
//
// 先空跑一次做预热，避免把一次性初始化（缓冲池、map 扩容）算进稳态开销。
func allocatedBytesPerRun(t *testing.T, runs int, fn func()) float64 {
	t.Helper()
	fn()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for index := 0; index < runs; index++ {
		fn()
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(runs)
}
