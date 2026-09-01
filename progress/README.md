# Gotato 阶段实施计划

| 项 | 值 |
| --- | --- |
| 基线日期 | 2026-09-01 |
| 编号规则 | 沿用一阶段里程碑 M0–M6，新增阶段从 M7 起排 |
| Slice 来源 | `specs/12-delivery-roadmap.md` 的 Slice 1–7 |
| 一阶段原始计划 | 已按里程碑拆入本目录各阶段文件，根目录不再保留单独文档 |

## 1. 目录用法

- 本页只放索引、状态总表、跨阶段闸门、风险与待拍板项
- 一个阶段一个文件，推进某个阶段只改那一个文件
- 每个交付项一个复选框，勾选时同步改本页状态总表对应行
- 已完成项带代码 pin（`文件:行号`），便于回原始现场核对
- 阶段文件里的退出条件是该阶段能否宣布完成的唯一判据，验收命令必须全绿

## 2. 状态总表

| 阶段 | 名称 | 对应 Slice | 状态 | 未完成项 |
| --- | --- | --- | --- | --- |
| [M0](M0-library-skeleton.md) | Library 骨架与依赖方向 | Slice 1 | 已完成 | 无 |
| [M1](M1-core-single-agent.md) | Core 单 Agent 与 Run Events | Slice 1 · 3 | 已完成 | 无 |
| [M2](M2-tools-and-close.md) | Tool 契约与 Close 语义 | Slice 2 · 3 | 已完成 | 无 |
| [M3](M3-gateway-adapter.md) | gotato-gateway LLM 适配器 | Slice 2 | 已完成 | 凭据落盘补测，不阻塞 |
| [M4](M4-orchestration.md) | 进程内 Orchestration | Slice 5 | 已完成 | 无 |
| [M5](M5-host-and-sse.md) | Host 与 HTTP/SSE 适配器 | Slice 3 · 6 | 已完成 | 无 |
| [M6](M6-service-and-blackbox.md) | 服务组装、drain 与黑盒验收 | Slice 6 | 已完成 | 无 |
| [M7](M7-core-control-commands.md) | Core 控制命令与 Continue | Slice 4 | 已完成 | 无 |
| [M8](M8-extensions.md) | Extensions 六个挂点 | Slice 4 | 已完成 | 无 |
| [M9](M9-toolsets-and-parallel.md) | ToolSets 与有界并行 Tool | Slice 4 | 已完成 | 无 |
| [M10](M10-orchestration-policies.md) | 层间边界与容量 | Slice 5 收口 · 7 | 已完成 | 无 |
| [M11](M11-agent-routines.md) | Agent Routines 与 spawn | Slice 7 | 已完成 | routine 事件投影并入 M13 |
| [M12](M12-durable-conversation.md) | Conversation 持久化与重启恢复 | Slice 7 | 已完成 | 无 |
| [M13](M13-wire-contract.md) | wire contract 冻结与第二协议适配器 | Slice 6 收口 · 7 | 未开始 | 全部 |

Slice 与里程碑不是一一对应：

- Slice 1 由 M0 与 M1 交付
- Slice 2 的 Tool 侧在 M2，LLM 侧在 M3
- Slice 3 的 Core 事件在 M1，deadline 与 abort 在 M2，远端投影在 M5

## 3. 结构不变量

每个阶段都不得破坏，改动触碰到这几条就是设计错误而不是实现细节：

```text
Core 不依赖 host、orchestration、HTTP、SSE、JSON wire type、provider SDK
一个 canonical Agent Loop，Orchestration 与 Host 不得复制它
Agent 是私有状态的 Go 执行单元，一次只处理一个 Prompt 或 Continue
Run settlement 与 Agent closure 是两件事
一个 Run 恰好一个终态 agent_end，之后不再启动任何工作
Agent lifecycle 信号不进入 Run 的 Event sequence
所有本地工作有显式 Context、上界和终态
LLM 与 Tool 集成走 adapter 进入
cmd/gotato-agent 只做依赖注入与进程信号处理
```

依赖方向：

```text
gotato 根包          Core 契约与实现
orchestration     →  gotato
host              →  orchestration + gotato
gateway           →  gotato 的 Model 契约
cmd/gotato-agent  →  host + orchestration + gateway + internal/testmodel
```

## 4. 推进顺序

```text
M2/M5/M6 收尾（已完成）
        ↓
M7 控制命令 → M8 Extensions → M9 ToolSets 与并行 Tool（已完成）
        ↓
M10 层间边界与容量（已完成）
        ↓
M11 Agent Routines ┊ M12 Conversation 持久化（已完成）
        ↓
M13 wire contract 冻结与第二协议适配器  ← 下一站
```

M7 到 M9 是串行做完的。三者都落在 `agent.go:720` 的 `executeRun` 上：M7 改 Loop 的命令入口，M8 改 Loop 的阶段挂点，M9 改 Tool 调度层。

M11 与 M12 之间没有依赖，可以并行。两者都要等 M10 把 Orchestration 的容量与策略边界先定下来。

### 收尾清单

M7 开工前要清的四项已经清完：

- [x] M2：typed 函数 Tool helper，`WithFunc` / `NewFuncTool`（`toolfunc.go`）
- [x] M5：SSE 断连断言测试（`host/lifecycle_test.go:85`、`:121`）
- [x] M6：drain 断言测试（`host/lifecycle_test.go:146`）
- [x] M6：真实本地进程 black-box 测试（`cmd/gotato-agent/main_test.go:22`）

## 5. 一阶段完成闸门

一阶段完成定义共 18 条，2026-09-01 全部达标：

| 条目 | 状态 | 依据 |
| --- | --- | --- |
| 本地 Agent 可以一条命令启动 | 达标 | `cmd/gotato-agent/main.go:40` |
| 默认运行不依赖外部 Model、DB、Registry、Broker | 达标 | 默认 `-model echo`，`cmd/gotato-agent/main.go:42` |
| Prompt 可以返回确定结果 | 达标 | `agent_test.go:66` |
| Core Loop 只有一个实现 | 达标 | `agent.go:528` 是唯一 loop |
| 一个 Agent 不会并发处理两个 Prompt | 达标 | `agent_test.go:220` |
| Run settlement 与 Agent closure 分离 | 达标 | `agent.go:720` 与 Close 路径分离 |
| Close 幂等且 Context 有界 | 达标 | `agent_test.go:245` |
| 阻塞 Model 或 Tool 时 Closing 状态可观测 | 达标 | `agent_test.go:245` |
| ConversationKey 可以稳定路由 | 达标 | `orchestration/orchestrator.go:181` |
| 退休不会产生双重 live Agent | 达标 | `orchestration/orchestrator_test.go:86` |
| Dormant Conversation 可以在进程内 rehydrate | 达标 | `orchestration/orchestrator_test.go:50` |
| rehydration 使用新 AgentID 和新 generation | 达标 | `orchestration/orchestrator.go:242` |
| 旧 generation 被拒绝 | 达标 | `orchestration/orchestrator_test.go:75` |
| SSE Event 有序且有界 | 达标 | `host/server_test.go:274`，订阅缓冲 `events.go:108` |
| Host drain 不会虚报未完成的关闭 | 达标 | `DrainIncomplete` 报告未结算的 Conversation（`orchestration/orchestrator.go:623`），用例 `host/lifecycle_test.go:146` |
| `go test ./...` 通过 | 达标 | 2026-09-01 全绿 |
| `go test -race ./...` 通过 | 达标 | 2026-09-01 全绿 |
| 至少一条真实本地进程 black-box 测试通过 | 达标 | `cmd/gotato-agent/main_test.go:22` |

## 6. 风险登记

| 风险 | 处理方式 | 归属阶段 |
| --- | --- | --- |
| 把本地 HTTP API 当成最终 wire contract | endpoint 在代码里标记为本地实验性质，wire JSON 不进 Core，契约单独冻结 | M13 |
| 把内存 snapshot 当成进程恢复能力 | 一阶段只验证内存内的退休与 rehydration，崩溃恢复归 M12 | M12 |
| Close 无法强杀忽略 Context 的 Model 或 Tool | 所有接口传 Context，测试故意用不响应 Context 的 fake work，超时后保留可观测的 Closing | M2 已处理 · M6 |
| Event backpressure 反压破坏 Core | Protected 与 Coalescable 从第一版就分类，bridge 有界，队列满行为显式 | M1 已处理 · M5 |
| 退休竞态产生两个 live Agent | 路由状态转换与 generation fencing 由单一 authority 管理，并发用例加 race detector 验证 | M4 已处理 · M10 |

## 7. 待拍板

推进到对应阶段之前需要定下来，否则那个阶段没法开工：

| 决策 | 现状 | 阻塞阶段 |
| --- | --- | --- |
| gRPC 是否仍是目标协议 | `specs/09-agent-service-and-grpc.md` 以 gRPC 立契约，实现是纯 `net/http`，`go.mod` 无相关依赖 | M13 |
| 账本该拆成几种类型 | 层间泄漏已堵，剩下的是「要不要把约定写进编译器」。`specs/01` 的 `AgentState`、`Turn` 与 `specs/02` 的 `ModelMessage` 在代码里仍不存在 | 待议 |

### 已定的决策

M7 到 M9 期间定下来的四条，落在对应阶段文件里：

| 决策 | 结论 | 出处 |
| --- | --- | --- |
| 是否冻结 `AgentLifecycle` 为正式接口 | 不冻结。控制能力走 `ControllableAgent`，最小 `Agent` 保持两个方法 | [M7](M7-core-control-commands.md) |
| Busy 时 direct Prompt 的默认行为 | 保持类型化 busy 错误。要排队或改道的走 Steer 与 FollowUp | [M7](M7-core-control-commands.md) |
| JSON Schema 支持子集的边界 | 强制 type、properties、required、additionalProperties，其余关键字透传不校验 | [M9](M9-toolsets-and-parallel.md) |
| 并行 Tool 的默认上界 | `MaxParallelTools` 默认 1，并行需显式开启 | [M9](M9-toolsets-and-parallel.md) |
| rehydrate 时校验 Agent definition 版本 | 不加。外部没有资格对 Agent 提要求，`docs/00` §2 的立场是 Agent 自己拥有它的工作 | [M10](M10-orchestration-policies.md) |
| `AfterIdle` 的 TTL 默认值 | 不设框架默认值。这是配置项，由接入方按场景填 | [M10](M10-orchestration-policies.md) |
| `StreamingAgent` 接口 | 不补。它在 `docs/03` 与 `specs/10` 里只是两段接口草图，没有任何场景、调用方或用例；真实消费者是 SSE 端点，走 `Subscribe` 已经够用 | 文档待改 |
