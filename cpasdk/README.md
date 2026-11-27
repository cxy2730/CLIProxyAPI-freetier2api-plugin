# cpasdk

本目录是 CLIProxyAPI 宿主 ABI/JSON 契约的 **vendored 副本**，不是第三方依赖：

- `pluginabi/types.go` 对齐宿主 `sdk/pluginabi/types.go`；
- `pluginapi/types.go` 对齐宿主 `sdk/pluginapi/types.go`。

**当前对齐版本：CLIProxyAPI v8.1.8（`SchemaVersion = 11`，ABI 仍为 1）。**

v8.1.8 把契约升到 11，新增的都是 **scheduler 插件侧**的能力：

- `Capabilities.SchedulerPickCancel`：调度插件可订阅 `scheduler.pick.cancel` 推送，
  请求上下文被取消时宿主主动通知，不必再阻塞等 `scheduler.pick` 返回
- `scheduler.pick` 请求新增 `host_callback_id`，配合 `host.context.done` /
  `host.context.wait` 两个宿主回调，让插件观察请求是否已取消或超时
- 新增类型：`SchedulerPickCancelRequest`、`HostContextDone/WaitRequest|Response`

本插件不实现 `SchedulerPick`，也未声明 `SchedulerPickCancel`，因此这些能力只做契约
同步、不参与运行；协商逻辑仍是 `min(宿主版本, 11)`，老宿主照旧降级到 10/8/6。

v8.1.4 起 `Capabilities` 多了一个 `ExecutorAuthProviders []string`（宿主把 Home 选中的
凭证透传给插件 Executor，见宿主 `internal/pluginhost/executor_route.go`）。本插件的凭证
全部由插件自己管理，因此该字段恒为空、不参与 Home 分发——同步时保留字段、不填值。

同步方式：从宿主对应版本复制这两个文件，保持排版一致；`gofmt` 检查刻意排除本目录。
宿主升级 ABI 时先更新这里，再调整 `internal/app` 的 RPC 适配层。

## 同步时必须手工回填的本地扩展

上游没有这两处，直接覆盖会把它们删掉（编译期就会报错，别等到运行）：

1. `pluginapi.QuotaFetchResponse.Claim` + `pluginapi.ClaimStatus`
   控制台的「领取套餐」按钮靠它渲染。
2. `pluginapi.ManagementRequest.Context` / `RequestContext()`
   只给测试注入请求上下文用（`json:"-"`，不上线传输）。

## 与上游的唯一实现差异

上游两个文件都 import `github.com/MoeForge/moego/json`（含 `ojson` 零拷贝解析）。
本仓库不引入第三方依赖，统一改成 `encoding/json`；`parseExcludeCandidatesJSON`
用 `encoding/json` 等价重写（该函数只服务 `SchedulerContinuity`，本插件未实现）。

## 契约版本协商

插件声明的 `schema_version` 不能写死成最新版本：宿主侧校验是
「插件版本 > 宿主版本就拒绝加载」，而 CPA 8.0.0 是 6、8.1.0 是 10。
`internal/app` 的 `negotiatedSchemaVersion` 会按宿主在 `plugin.register` /
`plugin.reconfigure` 里送的 `schema_version` 降级，跨版本分发同一份 `.so` 才不会
在老宿主上直接加载失败。

新增能力（v7 omit_body、v8 裸 body 补丁、v9 多供应商模型、v10 多 auth 标识）目前
**一项都没声明**：本插件是单 provider + executor 架构，请求体改写发生在各供应商的
executor 里（别名→官方模型名按供应商各自规则），宿主侧的 body 补丁钩子拿不到这份
规则，声明了也用不上。要声明前先确认 `internal/app/schema_negotiation_test.go`
里的能力/方法一致性用例仍然通过。

## 宿主请求缓冲区契约（`cliproxy_plugin_api.call` 的 `request`）

- 宿主侧约定：`request` / `request_len` 指向的内存在**本次同步调用期间**有效，且不会被并发改写；
  调用返回后插件不得再访问它。这条是宿主侧承诺（宿主源码不在本仓库）。
- 插件侧默认用 `C.GoBytes` 复制一份再处理。
- 例外：`response.normalize_before` 在根目录 `cgo.go` 的 `hostRequestBytes` +
  `isReadOnlyRequestMethod` 里直接借用宿主缓冲区（每个流式分片都会带着两份完整请求体，
  复制开销与请求大小成正比）。
- 因此进入该名单的方法必须满足：**只读**（不修改入参）、**不持有**（不保存任何切片片段）、
  **不异步**（不在调用返回后的 goroutine 里访问）。新增方法前先确认这三条。
