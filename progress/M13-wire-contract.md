# M13 — wire contract 冻结与第二协议适配器

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 6 收口 · Slice 7（cross-process adapters） | M7、M11、M12 |

## 目标

把本地测试用的 HTTP/SSE 投影升级成冻结的 wire contract，并用第二个协议适配器证明 Host 的语义边界确实与协议无关。

## 做法：先抽边界，再加协议

改之前 HTTP handler **就是** Host——语义和编码是同一段代码。这种形状下加第二个适配器，长出来的一定是第二套语义。所以顺序是：

```text
1. 抽出 host.Service：协议无关的语义边界，带冻结的契约版本
2. HTTP handler 退化成对它的薄映射
3. 断言 Embedded 与 Hosted 走出同一条 canonical 事件序列
4. 加 gRPC 适配器，映射到同一个 Service
```

第三步是整个分层押的注：**Host 加的是寻址与投递，不是第二套 Agent 语义**。这条以前从来没被断言过。

## 交付项

- [x] `host.Service`：协议无关的边界，`ContractVersion` 冻结契约版本
- [x] HTTP handler 全部改成对 `Service` 的薄映射，wire JSON 止步于 handler
- [x] `Command` 恰好带一个输入：Prompt 或 Continue，两者都给或都不给是参数错误
- [x] Agent close 与 retirement 是独立的 Host 生命周期操作，不是流关闭的副作用
- [x] 投递失败只结束投递，Run 的结算与 Agent 的可用性不受影响
- [x] `ProjectedEvent` 保留 Core 身份、事件分类、correlation 与顺序，路由元数据只是附加
- [x] Embedded 与 Hosted 等价性测试
- [x] gRPC 适配器落地，与 HTTP 共用同一个 `Service`
- [x] wire 类型不进 Core：proto 类型只存在于适配器模块

## gRPC 为什么是独立模块

`docs/00` 第 3 节说基础设施是外部的、可替换的；`docs/03` 第 1 节说最小 Agent 路径不需要服务平台。把 grpc-go 塞进根模块，等于让每个只想在自己进程里跑一个 Agent 的调用方都背上它的依赖图。

所以 `adapter/grpc/` 是独立的 Go module，根模块的直接依赖仍然只有 `yaml.v3` 一个。要 gRPC 的部署 import 它，不要的不受影响。

## 为什么是 server-streaming 而不是双向流

`specs/09` 的草案画的是双向流 `rpc Run(stream RunCommand) returns (stream RunEvent)`，用来承载 Start、Steer、FollowUp、Cancel。

但 Steer 与 FollowUp 现在只在 Core 上，还没进 `host.Service`（M7 的遗留）。双向流除了 Start 之外没有东西可送，而 Cancel 已经有自己的 RPC。所以这一版是 server-streaming，终态 outcome 作为流上最后一条消息。

等 Steer 与 FollowUp 进了 `Service`，命令通道再升成双向流，那时 `specs/09` 第 5 节的 Start/Active/Terminal 协议生命周期才有实际约束对象。

## routine 事件（承 M11）

M11 把 `routine_started` 等四个事件推到这里，理由是 `docs/04` 第 10 节：被派生的 Agent 有自己的事件序列，投影是 Host 的事。

现状是 `RunEvent` 的 wire 形状已经带了 `spawn_id` 与 `origin_run_id` 两个 correlation 字段，投影所需的载体齐了。真正把子 Agent 的事件投到源头流上，需要 Orchestration 侧一个跨 Agent 的事件观察边界——那是 Slice 7 的 `Event projection and delivery bridges`，本阶段没做。

## 退出条件

```text
wire contract 有版本与冻结说明                                    ✅
两个协议适配器共用同一个 Host 语义接口，没有第二套 Agent 实现      ✅
同一场景在 Embedded 与 Hosted 两条路径产出相同 canonical 事件序列  ✅
关闭投递流不会被报告成 Conversation 已关闭                        ✅
Core 包依旧不 import 任何 wire 类型                               ✅
Protected Event 在 wire 层保序                                    ✅
```

## 测试

| 用例 | 断言 |
| --- | --- |
| `TestEmbeddedAndHostedProduceTheSameEventSequence` | 同一脚本场景，直连 Core 与走 Host 的事件序列逐个相同 |
| `TestProjectedEventsCarryRoutingMetadata` | 路由元数据是附加的，没有覆盖 Core 字段 |
| `TestCommandRejectsAnAmbiguousInput` | 输入既不给也不能都给 |
| `TestDeliveryFailureDoesNotDecideRunSettlement` | 投递失败之后 Agent 仍可用 |
| `TestGRPCReportsTheSameContract` | 两个适配器报同一个契约版本 |
| `TestGRPCRunSettlesThroughTheHostBoundary` | Run 走 wire 之后身份与终态不丢 |
| `TestGRPCStreamPreservesEventOrderAndEndsWithTheOutcome` | 事件序号严格递增且从 1 起，终态在最后一条 |
| `TestGRPCRejectsAnAmbiguousCommand` | 参数错误映射成 `InvalidArgument` |
| `TestGRPCLifecycleCommandsAreSeparateFromTheStream` | 退休与复活走独立 RPC |
| `TestGRPCConversationRecordCarriesNoTranscript` | wire 记录同样不带 transcript |

## 验收命令

根模块与适配器模块分开跑：

```bash
go test -race ./...
cd adapter/grpc && go test -race ./...
```

proto 改动之后重新生成：

```bash
cd adapter/grpc && buf generate
```
