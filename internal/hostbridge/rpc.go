// Package hostbridge 是插件与 CPA 宿主通信的唯一通道。
//
// 它把「宿主回调」与「出站 HTTP」收在一起，因为两者本质是同一件事：
// 插件的一切对外请求都得经过宿主，否则会绕过全局代理、账号级代理、
// 出站日志与 API 统计（表现为「代理配了没用」「请求日志里看不到」）。
//
// 四件事：
//
//  1. 回调通道（rpc.go / auth.go / management.go）：Call / CallCtx、凭证枚举、
//     管理请求上下文。CGO 实现由 package main 注入（//export 只能出现在 main 包），
//     测试用内存假宿主替换同一注入点。
//  2. 并发准入：宿主回调在当前 ABI 下不可取消，超时的调用仍会占用一个 OS
//     线程直到宿主返回，必须限流（默认 64）。
//  3. 关闭排空：Shutdown 时先禁止新调用，再等待在飞调用返回，最后才允许
//     宿主 dlclose 本动态库（在活跃 Go/CGO 栈上卸载会直接崩溃）。
//  4. 出站 HTTP（http.go / sse.go）：Client 一次性取完整响应；StreamClient
//     按需从 host.http.stream_read 拉取（SSE 对话）；ForwardRawSSE 转发原始流。
//
// 对 internal 包的依赖方向：本包不知道宿存在，宿主调用能力由 package main 在
// init 里通过 Configure 注入（避免 internal 反向 import main）。

package hostbridge

// Package hostabi 是插件侧调用宿主回调（host.*）的唯一通道。
//
// 它负责三件事：
//  1. 并发准入：宿主回调在当前 ABI 下不可取消，超时的调用仍会占用一个 OS
//     线程直到宿主返回，必须限流（默认 64）；
//  2. 关闭排空：Shutdown 时先禁止新调用，再等待在飞调用返回，最后才允许
//     宿主 dlclose 本动态库（在活跃 Go/CGO 栈上卸载会直接崩溃）；
//  3. 回调作用域：host_callback_id 绑定到 context，宿主据此把上游请求/响应
//     关联回原始请求，并在请求结束时回收流资源。
//
// CGO 实现由 package main 注入（//export 只能出现在 main 包里）；
// 测试用内存假宿主替换同一注入点。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"freetier2api-plugin/internal/logger"
)

// Caller 是宿主回调的底层实现（由 main 注入 CGO 版本，测试注入内存版本）。
type Caller func(callbackID, method string, payload any) (json.RawMessage, error)

// ErrNotConfigured 表示宿主回调实现尚未注入。
var ErrNotConfigured = errors.New("hostabi: caller is not configured")

const (
	// maxConcurrentCalls 是在飞宿主调用的上限。
	maxConcurrentCalls = 64
	// shutdownDiagnosticInterval 是关闭排空期打印诊断日志的间隔。
	shutdownDiagnosticInterval = 5 * time.Second
)

var (
	callerMu sync.RWMutex
	caller   Caller

	gateMu       sync.Mutex
	shuttingDown bool
	inflightWG   sync.WaitGroup
	gate         = make(chan struct{}, maxConcurrentCalls)
)

// Configure 注入宿主回调实现。由 main 在 init 里调用一次。
func Configure(impl Caller) {
	callerMu.Lock()
	caller = impl
	callerMu.Unlock()
}

func current() (Caller, error) {
	callerMu.RLock()
	impl := caller
	callerMu.RUnlock()
	if impl == nil {
		return nil, ErrNotConfigured
	}
	return impl, nil
}

// Call 发起一次宿主回调。
//
// callbackID 为空表示后台任务（无请求作用域）。优先使用 CallCtx，
// 让回调 ID 跟随 context 传递。
func Call(callbackID, method string, payload any) (json.RawMessage, error) {
	if errAcquire := acquire(); errAcquire != nil {
		return nil, errAcquire
	}
	defer release()
	impl, errCaller := current()
	if errCaller != nil {
		return nil, errCaller
	}
	return impl(callbackID, method, payload)
}

// CallCtx 发起一次宿主回调，callback ID 从 context 取。
func CallCtx(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	return Call(CallbackID(ctx), method, payload)
}

// acquire 申请一个宿主调用槽位。
//
// 准入检查与 WaitGroup.Add 必须在同一把锁内完成，否则 Wait 可能观察到
// 零计数，导致关闭排空提前返回并在活跃栈上 dlclose。
func acquire() error {
	gateMu.Lock()
	if shuttingDown {
		gateMu.Unlock()
		return errors.New("host callbacks unavailable: plugin is shutting down")
	}
	inflightWG.Add(1)
	gateMu.Unlock()
	gate <- struct{}{}
	return nil
}

func release() {
	<-gate
	inflightWG.Done()
}

// Inflight 返回当前在飞的宿主调用数。
func Inflight() int { return len(gate) }

// WaitForShutdown 关闭准入并等待所有在飞宿主调用返回。
//
// 这里**刻意不做有界等待**：宿主回调不可取消，提前返回会在仍然执行的
// Go/C 栈上触发 dlclose。等待期间每 diagEvery 输出一次诊断。
func WaitForShutdown(diagEvery time.Duration) {
	gateMu.Lock()
	shuttingDown = true
	gateMu.Unlock()

	if diagEvery <= 0 {
		diagEvery = shutdownDiagnosticInterval
	}
	if Inflight() == 0 {
		inflightWG.Wait()
		return
	}
	done := make(chan struct{})
	go func() {
		inflightWG.Wait()
		close(done)
	}()
	ticker := time.NewTicker(diagEvery)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			logger.Error("关闭：等待进行中的宿主回调（在途=%d 已等待=%s，宿主回调无法取消）",
				Inflight(), time.Since(start).Round(time.Millisecond))
		}
	}
}

type callbackIDKey struct{}

// WithCallbackID 把宿主回调 ID 绑定到 context。
func WithCallbackID(ctx context.Context, callbackID string) context.Context {
	trimmed := strings.TrimSpace(callbackID)
	if trimmed == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, callbackIDKey{}, trimmed)
}

// CallbackID 从 context 取宿主回调 ID。
func CallbackID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(callbackIDKey{}).(string)
	return id
}

// MarshalPayload 把载荷编码为 JSON，并按需注入 host_callback_id。
//
// 载荷是 any（各方法自有的请求结构体），因此采用「先序列化再塞字段」的方式，
// 而不是给每个结构体都加一个字段。
func MarshalPayload(callbackID string, payload any) ([]byte, error) {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload: %w", errMarshal)
	}
	if callbackID == "" {
		return raw, nil
	}
	fields := map[string]json.RawMessage{}
	if len(raw) > 0 && string(raw) != "null" {
		if errUnmarshal := json.Unmarshal(raw, &fields); errUnmarshal != nil {
			return nil, fmt.Errorf("decode host callback payload: %w", errUnmarshal)
		}
	}
	encodedID, errID := json.Marshal(callbackID)
	if errID != nil {
		return nil, fmt.Errorf("marshal host callback id: %w", errID)
	}
	fields["host_callback_id"] = encodedID
	return json.Marshal(fields)
}

// DecodeEnvelope 解开宿主回调返回的信封，取出 result 部分。
func DecodeEnvelope(raw []byte) (json.RawMessage, error) {
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result,omitempty"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode envelope: %w", errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback failed")
	}
	return append(json.RawMessage(nil), env.Result...), nil
}
