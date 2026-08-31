# Gotato 一阶段实施计划

**状态：** 实施计划草案
**位置：** 仓库根目录，不属于 `docs/` 架构文档
**目标：** 交付一个可以在本地启动、可以通过 HTTP/SSE 访问、可以观察 Agent 生命周期的 Gotato Reference Agent。
**当前进度：** Library Core、gotato-gateway、进程内 Orchestration、Host、HTTP/SSE adapter 和本地服务已实现；正在通过集成测试和本地启动验证。

## 1. 一阶段目标

一阶段不以“完成全部 Core 规范”或“完成生产级 Hosted 平台”为目标，而是交付一个真实可运行的最小闭环：

```text
go run ./cmd/gotato-agent
        ↓
本地 HTTP/SSE Protocol Adapter
        ↓
Host
        ↓
进程内 Orchestration
        ↓
Agent Core
        ↓
Deterministic Model
```

这个程序应当能够被开发者启动、调用、观察和关闭，并且能够验证当前设计中最重要的语义：

```text
Prompt
  → canonical Model → Tool → Model Loop
  → RunResult / Events
  → Run 结束但 Agent 保持可用
  → Conversation 路由
  → Agent retirement
  → Conversation Dormant
  → 新 AgentID / AgentGeneration rehydration
  → Agent Close
```

一阶段的本质不是写一个 Demo，而是建立后续实现和黑盒测试共同依赖的 **可运行参考实现**。

## 2. 一阶段成功标准

完成一阶段后，以下命令应当可以启动本地服务：

```bash
go run ./cmd/gotato-agent --addr 127.0.0.1:8787
```

默认启动不依赖：

```text
真实 LLM 服务
API Key
数据库
Redis
gRPC
Kubernetes
Service Registry
Message Broker
```

默认使用 Deterministic Model，使测试结果不受网络、供应商、采样参数和外部服务影响。真实 Model Adapter 不是一阶段的必要条件。

必须能够用 `curl` 或一个很小的 Go 客户端完成：

1. 创建或解析一个 Conversation。
2. 提交 Prompt 并得到 RunResult。
3. 通过 SSE 或本地 Event API 观察 canonical Events。
4. 确认 Run 结束后 Agent 仍处于 `Idle`。
5. 使用相同 ConversationKey 再次访问同一个 live Agent。
6. 显式退休 Agent，使 Conversation 进入 `Dormant`。
7. 再次访问该 Conversation，并得到新的 AgentID 和递增的 AgentGeneration。
8. 确认旧 handle/generation 不会继续接收新请求。
9. 显式关闭 Agent，并确认新的 Prompt 被拒绝。
10. 触发 drain，确认新请求停止、现有请求按策略结束。

## 3. 范围

### 3.1 必须实现

#### Agent Core

- `Agent` 最小接口：`Prompt` 和 `Close`。
- Agent 私有状态和单一执行权限。
- 一个 Agent 同时只处理一个 Prompt/Continue。
- canonical Model → Tool → Model Loop。
- Deterministic Model 和 Model stream。
- 基础 Message、ToolCall、ToolResult、RunResult。
- Tool 参数完整组装和基础验证。
- Tool executor 单个 ToolUse 最多执行一次。
- Context cancellation 和基本本地限制。
- canonical Run Events。
- `agent_end` 恰好一次，且只代表 Run 结束。
- `Created → Idle ⇄ Busy → Closing → Closed` 生命周期。
- 幂等、Context 有界的 `Close`。
- Busy、Closing、Closed 的明确错误分类。

#### 进程内 Orchestration

- Agent definition/factory。
- ConversationKey 到 ConversationRecord 的映射。
- live Agent handle retention。
- per-Conversation create-or-resolve 串行化。
- admission 和 single-flight dispatch。
- `Retain`、显式 retirement、`Ephemeral` 的最小策略。
- Agent retirement 状态 `Retiring`。
- in-memory snapshot store。
- Conversation `Dormant` 和 rehydration。
- 新 AgentID 和 AgentGeneration。
- stale generation/handle fencing。
- 退休期间禁止并发 rehydration。
- 生命周期信号的最小本地观察能力。

#### gotato-gateway Library

- OpenAI-compatible HTTP Gateway adapter，实现 Gotato `Model` 接口。
- YAML 配置文件加载和环境变量占位符展开。
- API base URL / complete endpoint 配置。
- API Key 和自定义 headers。
- SSE response decoding。
- text、usage、finish reason normalization。
- streaming Tool Call assembly 和可逆 Tool ID 映射。
- 只在 stream 建立前进行的 bounded retry。
- Provider HTTP error 的独立分类。
- `httptest` Gateway fixture。

#### 本地 Host

- 标准库 `net/http` 实现的本地 Host。
- JSON wire types 与 Core types 分离。
- 同步 Run API。
- SSE Event API。
- Conversation 查询。
- Agent 状态查询。
- Agent close/retire 测试入口。
- `/healthz`、`/readyz` 和 drain。
- SIGINT/SIGTERM graceful shutdown。

#### 测试

- Core unit tests。
- Orchestration unit tests。
- Host handler tests。
- `go test -race ./...`。
- 启动真实本地进程的 black-box 测试或脚本。
- 阻塞 Model/Tool、取消、背压、退休失败等故障测试。

### 3.2 明确不在一阶段

以下能力不因为本地服务存在就被提前承诺：

```text
具体供应商 SDK Adapter（OpenAI-compatible gotato-gateway 除外）
完整 JSON Schema 标准实现
ToolSet staged discovery
全部 Extensions
复杂 Steer / FollowUp 策略
跨进程 Agent placement
数据库持久化
进程重启后的 Conversation 恢复
多 Pod 路由
Durable Run checkpoint/resume
可恢复的 Tool side effect
Gateway 之外的认证、租户、计费和治理
生产级 gRPC wire contract
```

一阶段只用 in-memory store 验证退休、snapshot 和 rehydration 的语义顺序；它不宣称支持进程崩溃后的恢复。

## 4. 建议代码布局

第一阶段必须先建立并验证 Gotato Library，再由该 Library 组装本地服务。`cmd/gotato-agent` 不得直接实现 Core 语义；它只能依赖公开的 Core、Orchestration 和 Host 包。包名可以在实现时确认，但依赖方向必须保持如下结构：

```text
go.mod

types.go / model.go / tool.go / events.go / errors.go / limits.go
                       gotato 根库的公共 Core 契约
agent.go               gotato 根库的 Core Agent 实现
gateway/                gotato-gateway LLM Gateway adapter
orchestration/         Conversation、routing、retirement、rehydration
host/                  Host semantic service boundary + HTTP/SSE adapter
internal/testmodel/    Deterministic Model、scripted streams
cmd/gotato-agent/      基于 Library 的本地可启动服务
*_test.go              Library 和 Orchestration 的契约测试
```

依赖方向：

```text
gotato root package → Core public contracts and implementation
orchestration → gotato Agent/Core contracts
host → orchestration/gotato contracts
gateway → gotato Model contract
cmd/gotato-agent → host + orchestration + gateway + internal/testmodel
```

`cmd/gotato-agent` 只做组合和进程生命周期管理，不能实现 Core Loop。

Core 不得反向依赖 `host`、`orchestration`、HTTP、SSE、JSON wire type 或具体 Model Provider。

## 5. 一阶段实现边界

### 5.1 Core 最小公共接口

第一阶段以如下接口为实现基线：

```go
type Agent interface {
    Prompt(context.Context, Message) (RunResult, error)
    Close(context.Context) error
}
```

生命周期观察、事件流、snapshot、退休请求均作为 additive capability 或内部接口实现，不为了验证本地服务而把所有能力塞进最小 `Agent` 接口。

可以提供但暂不承诺最终命名的能力包括：

```go
type AgentLifecycle interface {
    Agent
    Status() AgentStatus
    Done() <-chan struct{}
}

type Snapshotter interface {
    Snapshot(context.Context) (CoreSnapshot, error)
}

type RetirableAgent interface {
    Agent
    RequestRetirement(context.Context, RetirementReason) error
}
```

`Snapshotter` 属于 Core 与 Orchestration 之间的实现边界；具体 snapshot 格式仍必须保留版本字段，并且不得包含 goroutine、channel、lock、Context 或不可恢复的 provider client。

### 5.2 Deterministic Model

默认 Model 不访问网络，至少支持：

```text
Echo response
Scripted final response
Scripted Tool Call → Tool Result → final response
Cancellable/blocking response
Protocol failure response
```

它必须能够在测试中精确控制：

- Model call 次数。
- 返回的 text/tool-call 顺序。
- completion 和 failure。
- 是否响应 Context。
- 调用是否被取消。

默认本地服务可以使用 Echo 场景，让第一次运行不需要任何配置；测试场景可以切换到 scripted model。

### 5.3 Core 事件

Run Event 的 `Sequence` 从 1 开始，每个 Run 独立递增。最小实现支持：

```text
agent_start
turn_start
message_start
message_end
assistant message / Tool Call
 tool_execution_start
 tool_execution_end
Tool Result commitment
turn_end
agent_end
```

Agent lifecycle signal 不混入 Run sequence。最小生命周期信号至少能够表达：

```text
agent_created
agent_closing
agent_closed
agent_retirement_requested
agent_retirement_failed
```

Protected Event 不得被静默丢弃。SSE bridge 必须有明确容量；一阶段可以采用“受保护事件优先、进度事件可合并”的简单策略。

### 5.4 Close 语义

`Close` 的行为必须满足：

```text
Idle/Busy → Closing
Closing → Closed
```

要求：

- 进入 `Closing` 后拒绝新 Prompt/Continue。
- 关闭请求与 command admission 在 Agent authority 内串行化。
- Busy 时允许当前 Run graceful settle，或按明确策略先 Abort。
- 所有本地资源只关闭一次。
- `Close` 不等待远程 Host delivery。
- 多个并发 Close 不得重复关闭 channel 或资源。
- Close Context 只限制调用方等待时间。
- Model/Tool 忽略 Context 时，Agent 保持可观察的 `Closing`，不得伪报 `Closed`。
- `Done` 如实现，只能在真正 `Closed` 后关闭一次。

Run 的错误和 `Close` 的错误必须分开。Run 失败不等于 Agent 关闭失败。

## 6. 本地测试协议

一阶段本地 HTTP/SSE 是测试和演示用 Protocol Adapter，不是最终生产 wire contract，也不应成为 Core API。

建议的临时 endpoint：

```text
GET  /healthz
GET  /readyz
POST /v1/runs
POST /v1/runs/{run_id}/cancel
POST /v1/runs/stream
GET  /v1/conversations/{conversation_id}
POST /v1/conversations/{conversation_id}/retire
POST /v1/agents/{agent_id}/close
POST /admin/drain
```

### 6.1 创建/运行请求

请求至少支持：

```json
{
  "agent_name": "default",
  "conversation_key": "local-test",
  "prompt": "hello"
}
```

也可以使用已有的 `conversation_id`。如果同时提供 ID 和 Key 且二者冲突，Host/Orchestration 必须拒绝请求。

响应至少包含：

```json
{
  "conversation_id": "conv-...",
  "agent_id": "agent-...",
  "agent_generation": 0,
  "run_id": "run-...",
  "status": "completed",
  "final_message": "..."
}
```

字段是本地协议投影，不得直接让 JSON 类型进入 Core。

### 6.2 SSE

`POST /v1/runs/stream` 用于观察一次 Run 的完整事件投影。SSE 断开是否取消 Run 必须由本地 Host 明确配置；一阶段默认建议：

```text
SSE 断开 → 停止远程投递
SSE 断开 ≠ 自动关闭 Agent
是否取消当前 Run 由单独的明确策略决定
```

SSE 事件必须保留：

```text
agent_id
run_id
sequence
event_class
conversation_id
agent_generation
kind
payload
```

Orchestration 添加的 ConversationID 和 AgentGeneration 是路由元数据，不改变 Core Event 的含义。

### 6.3 生命周期 endpoint

生命周期 endpoint 只用于本地测试 Orchestration 行为：

```text
retire Conversation
  → Retiring
  → snapshot store commit
  → Core Close
  → Dormant

next request
  → restore snapshot
  → new AgentID
  → generation + 1
  → dispatch
```

`/v1/agents/{agent_id}/close` 关闭的是一个 live Core Agent。若该 Agent 属于 retained Conversation，Host 必须明确报告该动作是否同时关闭 Conversation；不能仅凭 stream 或 Agent close 宣称 Conversation 已关闭。

### 6.4 Drain

`POST /admin/drain` 或进程信号触发：

```text
Serving → Draining
  → readiness=false
  → 停止新 admission 和新 Agent creation
  → active Run settle/cancel
  → 根据策略关闭 live Agents
  → flush 或 abandon delivery
  → Stopped
```

一阶段可以在 drain deadline 到期时返回 incomplete drain；不得把忽略 Context 的 Agent 标记为已关闭。

## 7. Orchestration 实现计划

### 7.1 In-memory record

第一阶段使用内存记录，语义等价于：

```go
type ConversationRecord struct {
    ID           ConversationID
    Key          ConversationKey
    AgentName    AgentName
    LiveAgentID  AgentID
    Generation   AgentGeneration
    Status       ConversationStatus
    StateVersion uint64
    CoreSnapshot []byte
}
```

`LiveAgentID` 的规则：

```text
Active       → 有 live Agent 时设置
Retiring     → 保留旧 live Agent，阻止 rehydration
Dormant      → 必须为空
Closed       → 必须为空
Archived     → 必须为空
```

### 7.2 Resolve/create

对于同一 ConversationKey：

- 同一时刻只能有一个 create-or-resolve 操作。
- 已有 live handle 时直接复用，不创建新 Agent。
- `Retiring` 时拒绝或有界重试，不得创建第二个 live Agent。
- `Dormant` 时等待 rehydration 完成后再 dispatch。
- `Closed`/不存在时遵循显式 create policy，不得静默复用被丢弃状态。

### 7.3 Retirement

Retention retirement 的实现顺序必须是：

```text
1. 原子地将 Conversation 标记为 Retiring
2. 停止新 admission
3. 等待或显式取消 current Run
4. 在 quiescent boundary 获取 Core snapshot
5. 提交带版本的 in-memory snapshot record
6. 调用 Agent.Close
7. 移除 live handle
8. 将 Conversation 标记为 Dormant
```

如果 snapshot 或 record commit 失败：

```text
不得报告 retirement 成功
不得允许并发 rehydration
默认保留 live route 或转入明确的 discard/error 状态
```

如果 Core close 超时但后台仍在 Closing：

```text
Conversation 保持 Retiring
不得创建新 Agent
Host 报告 incomplete retirement/drain
```

### 7.4 Rehydration

Rehydration 必须：

1. 读取 `Dormant` record。
2. 根据 `AgentName` 和 definition version 找到 factory。
3. 从 snapshot 创建新 Core Agent。
4. 分配新的 AgentID。
5. 递增 AgentGeneration。
6. 使用 compare-and-swap 或等价的进程内锁原子安装 live route。
7. 安装成功后才 dispatch 新请求。

若两个请求同时触发 rehydration，只有一个可以成功安装 live route；另一个必须复用已安装的 Agent，而不是产生第二个 Agent。

## 8. 实施里程碑

### M0 — Gotato Library 骨架

交付：

- `go.mod`。
- `gotato` Core 公共类型和接口。
- `model`、`tool`、`event` 的基础契约。
- `orchestration` 和 `host` 的包边界。
- 最小 package-level 编译测试。

验收：

```bash
go test ./...
go vet ./...
```

此里程碑不创建可执行服务。服务只能在 Library 契约稳定后组装。

### M1 — Library Core 单 Agent

交付：

- Agent interface。
- private state + command boundary。
- Prompt、RunResult、基础 Message。
- Deterministic Model。
- canonical Loop。
- single-flight 和 busy error。
- Context cancellation。
- 基础 Run Events。

验收：

- Prompt 能得到确定结果。
- 同时提交两个 Prompt 时不会并发修改 Agent state。
- Run 只产生一个 terminal `agent_end`。
- Run 结束后 Agent 仍然可以再次 Prompt。

### M2 — Tool 与 Close

交付：

- 基础 Tool contract。
- Tool Call assembly。
- at-most-once executor。
- Tool Result commitment。
- `Closing`/`Closed`。
- 幂等 Close。
- Busy Close、取消和 stuck Context 测试。

验收：

- scripted Tool Call 可以完成 Model → Tool → Model。
- malformed arguments 不执行 Tool。
- Close 后不产生新 Run。
- 并发 Close 只释放一次资源。
- 不响应 Context 的 Model/Tool 不会被伪造为已经关闭。

### M4 — In-memory Orchestration

交付：

- Agent Factory。
- ConversationRecord。
- key routing。
- handle retention。
- admission。
- explicit Retire。
- in-memory snapshot store。
- Dormant/re-hydration。
- AgentGeneration fencing。

验收：

- 同一 Key 不产生重复 live Agent。
- Run settlement 后仍可复用 Agent。
- Retiring 阶段拒绝新 dispatch。
- snapshot 失败不会报告成功退休。
- rehydration 使用新 AgentID。
- stale generation 不能提交新请求。

### M3 — gotato-gateway Library

交付：

- OpenAI-compatible HTTP Gateway adapter。
- YAML 配置文件加载和环境变量占位符展开。
- Base URL / complete endpoint 配置。
- API Key 和自定义 headers。
- SSE text、usage、finish reason normalization。
- streaming Tool Call assembly 和可逆 Tool ID 映射。
- stream 建立前的 bounded retry。
- Provider HTTP error 分类。
- Gateway `httptest` fixture。

验收：

- Core 只依赖 provider-neutral `Model` 接口。
- Gateway 请求包含正确的认证头、Model、Messages 和 Tools。
- text、usage、tool call、finish reason 能转换为 Core Model Events。
- 429/5xx 只在 stream 建立前按次数重试。
- Gateway 错误不会泄露 API Key。

### M5 — Library Host 与 SSE

这一里程碑仍然属于 Library：实现可复用的 Host 和 HTTP/SSE Protocol Adapter，不把 handler 逻辑复制到 `cmd`。

交付：

- HTTP semantic handlers。
- JSON adapter types。
- synchronous Run endpoint。
- SSE Run endpoint。
- status、retire、close endpoint。
- bounded delivery bridge。
- disconnect 和 delivery settlement 测试。

验收：

- `curl` 可以完成完整 Prompt。
- SSE 能观察 canonical Event order。
- SSE 断开不会隐式关闭 Agent。
- Host delivery settlement 与 Core Run settlement 独立。
- Protected Event 不被静默丢弃。

### M6 — `gotato-agent` 服务组装、启动与 Black-box Acceptance

这一里程碑才创建并启动 `cmd/gotato-agent`。它必须只负责依赖注入、配置和进程信号处理，不能包含 Agent Loop、Conversation 路由或退休实现。

交付：

- readiness transition。
- stop admission。
- active Run drain。
- Agent close/retirement composition。
- drain deadline 和 incomplete drain 状态。
- 本地进程 black-box 测试。
- `go test -race ./...`。

验收：

- drain 后 readiness 为 false。
- drain 后不创建新 Agent、不 dispatch 新 Run。
- deadline 到期不会虚报所有 Agent 已关闭。
- 完整测试流程不依赖外部服务。

## 9. 必须优先编写的测试场景

### 场景 A：最小单 Agent

```text
construct Agent
→ Prompt
→ assert final result
→ assert agent_end exactly once
→ Prompt again
→ Close
```

### 场景 B：单飞

```text
Prompt A blocks in Model
→ Prompt B
→ assert busy/not-available
→ release Model A
→ assert A settles once
```

### 场景 C：Busy Close

```text
Prompt blocks
→ Close with short Context
→ assert caller gets deadline/cancel error if wait expires
→ assert Agent remains observable as Closing
→ release Model
→ assert Done closes and status becomes Closed
```

### 场景 D：Conversation retirement

```text
resolve key
→ Prompt
→ assert AgentID = A, generation = 0
→ Retire
→ assert Conversation = Dormant
→ resolve same key
→ assert AgentID = B, generation = 1
→ assert A cannot dispatch
```

### 场景 E：并发 rehydration

```text
retire Conversation
→ concurrently resolve same key from N callers
→ assert exactly one new live Agent
→ assert all accepted requests use the same generation
```

### 场景 F：退休持久化失败

```text
make snapshot store fail
→ Retire
→ assert no successful retirement result
→ assert no rehydration
→ assert route remains safe and observable
```

### 场景 G：SSE 断开

```text
open SSE
→ start Run
→ disconnect client
→ assert delivery stops
→ assert Agent closure follows explicit policy only
```

### 场景 H：Drain

```text
start service
→ start active Run
→ trigger drain
→ assert readiness=false
→ assert new requests rejected
→ settle/cancel active Run
→ close or retire Agent
→ assert bounded completion or incomplete-drain report
```

## 10. 一阶段风险与处理方式

### Risk 1：把本地 HTTP API 误当成最终 API

处理：

- HTTP/SSE 只放在 Host/adapter 层。
- wire JSON 不进入 Core。
- endpoint 在代码中标记为 experimental/local。
- 最终 gRPC/HTTP contract 另行冻结。

### Risk 2：把 snapshot 当成进程恢复

处理：

- 一阶段只验证内存中的 retirement/rehydration。
- 明确关闭进程后内存 snapshot 丢失。
- 不宣称支持 crash recovery。
- Durable store 作为后续实现。

### Risk 3：Close 无法强杀阻塞 Model/Tool

处理：

- 所有接口传递 Context。
- 测试故意使用不响应 Context 的 fake work。
- Close 超时后保留 `Closing` 可观测状态。
- 不使用 goroutine 泄漏掩盖问题。

### Risk 4：Event backpressure 破坏 Core

处理：

- Protected 与 Coalescable 分类从第一版就存在。
- 所有 bridge 有界。
- queue full 行为显式定义。
- 不使用无界 goroutine/channel。

### Risk 5：退休竞态产生两个 Agent

处理：

- route 状态转换和 generation fencing 由 Orchestration 单一 authority 管理。
- 每个 key 的 create/resolve/re-hydrate 串行化。
- 对 `Retiring` 记录禁止新建 live Agent。
- 用并发测试和 race detector 验证。

## 11. 一阶段完成定义

只有同时满足以下条件，才认为一阶段完成：

```text
本地 Agent 可以一条命令启动
默认运行不依赖外部 Model/DB/Registry/Broker
Prompt 可以返回确定结果
Core Loop 只有一个实现
一个 Agent 不会并发处理两个 Prompt
Run settlement 与 Agent closure 分离
Close 幂等且 Context 有界
阻塞 Model/Tool 的 Closing 状态可观测
ConversationKey 可以稳定路由
退休不会产生双重 live Agent
Dormant Conversation 可以在进程内 rehydrate
rehydration 使用新 AgentID 和新 generation
旧 generation 被拒绝
SSE Event 有序且有界
Host drain 不会虚报未完成的关闭
go test ./... 通过
go test -race ./... 通过
至少一条真实本地进程 black-box 测试通过
```

## 12. 一阶段结束后的下一步

一阶段完成后再决定：

1. 冻结 Core lifecycle API 是否加入正式 `AgentLifecycle`。
2. 冻结 lifecycle signal 的独立类型和 channel contract。
3. 选择第一个真实 Model Adapter。
4. 补充正式 JSON Schema 子集。
5. 将 in-memory snapshot store 替换为可持久化实现。
6. 确定 Hosted wire contract 是否使用 gRPC、HTTP/Connect 或其他协议。
7. 设计多进程/多 Pod Conversation routing。

在这些问题解决前，不应把本地 Reference Agent 包装成“生产级分布式 Agent 平台”。
