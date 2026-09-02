# Gotato 改造实施计划

计划版本：1.0

`TODO.md` 记录问题、架构张力和定位决策。本文件负责确定实施顺序、阶段边界、验收门槛和延期条件。

## 1. 总览

Gotato 的核心链路保持不变：Core 维护一个有状态 Agent，Orchestration 管理 Conversation 与生命周期，Host 负责协议投影。改造过程中不新增第二套 Agent Loop。

当前优先级是先修复数据损坏和执行语义，再治理并发和生命周期，随后补齐长会话与持久化，最后收紧公共契约并扩展生态。

第一阶段产品目标是成为可嵌入 Go 服务的 Stateful Agent Runtime。这里的 Stateful Agent Runtime 指 Agent 在进程内持有 transcript、生命周期和控制命令，并能通过 snapshot 恢复 Conversation。

本轮不以 Hosted 多进程为前提。多 Pod 连续性、跨进程 placement、远程事件续传和完整 Hosted 平台只有在出现真实需求后才启动。

### 1.1 交付主线

```text
决策冻结与工程门禁
  ↓
阶段一：P0 语义与数据安全
  ↓
阶段二：并发、事件与生命周期
  ↓
阶段三：长会话与持久化
  ↓
阶段四：Core 契约与模块边界
  ↓
阶段五：Host、协议与生态
  ↓
阶段六：条件性 Hosted 多进程
```

### 1.2 版本门槛

| 版本 | 必须完成 | 可以延期 |
| --- | --- | --- |
| 修复版 | T01、T02、T03、T04、T05、T06 | 架构拆包、MCP、Hosted |
| Embedded Beta | 阶段一至阶段三、T08、T09、T10、基础 CI | 多进程、完整 Provider 广度 |
| Runtime v1 | 阶段四、阶段五、MCP、真实集成示例 | 阶段六 |
| Hosted 版本 | 阶段六全部交付 | 无 |

## 2. 架构边界与演进原则

### 2.1 当前责任链

```text
调用方
  ↓
Host Service
  ├── 同步 Run
  ├── Stream Run
  └── 生命周期操作
       ↓
Orchestrator
  ├── Conversation 路由
  ├── Agent admission
  ├── Retire / Restore
  └── SnapshotStore
       ↓
Core Agent Loop
  ├── Model
  ├── Tool
  ├── Control Command
  ├── Extension
  └── Canonical Event
```

Core 是 transcript、Run settlement 和 Agent 生命周期的唯一权威。Orchestration 不保存第二份 transcript。Host 不实现第二套 Agent 语义。

### 2.2 演进原则

- **先行为后结构。** 先修 T01 至 T10，再进行拆包和接口整理。
- **保留 Embedded 简洁路径。** 只使用一个 Agent 的调用方不需要依赖 Orchestration、Host 或协议模块。
- **显式区分状态。** Conversation 状态、Core transcript 版本和 Store CAS 版本不能继续使用含义相同的名称。
- **集中处理 Breaking change。** Message metadata、Event payload 和 ManagedAgent 等公共契约集中在阶段四处理，并同步更新 ContractVersion。
- **以测试作为里程碑门槛。** 每个阶段必须把临时探针转成正式测试，不能只依赖人工验证。

## 3. 决策前提

这些决策在阶段零完成。未完成时，不进入依赖它们的阶段。

| 编号 | 决策 | 推荐结论 | 影响范围 |
| --- | --- | --- | --- |
| D01 | Durable 的承诺范围 | 先做 Compaction 与 Run checkpoint，再评估 WAL | A03、A04、T07、T08 |
| D02 | Embedded 还是 Hosted | Embedded-first，Host 保留为参考部署 | A07、A10 |
| D03 | 与 Pi 的关系 | 作为独立项目，移除默认 Pi auth.json 耦合 | T11、gateway |
| D04 | 生态补齐顺序 | MCP ToolSet、Anthropic adapter、testkit、真实集成示例 | A02、A05、A06 |

### 3.1 D01 的具体口径

第一版只承诺以下恢复能力：

- 已退休 Conversation 可以跨进程重启恢复。
- Run 在结算边界完成 checkpoint 后，结算结果可以恢复。
- 进程在进行中的 Run 中退出时，不承诺从任意中间 Tool 调用继续执行。

只有在真实用户要求恢复进行中的 Run 时，才实现 append-only WAL 和 replay。

### 3.2 D02 的具体口径

阶段一至阶段五只优化单进程和可嵌入路径。阶段六的进入条件必须同时满足:

- 存在真实 Hosted 使用场景。
- 需求需要跨进程路由、故障转移或多 Pod Conversation 连续性。
- 阶段五退出评审确认现有内存路由已经成为实际限制。

未满足条件时，A10 保持延期，不提前引入分布式路由和租约系统。

## 4. 阶段执行顺序

## 阶段零：决策冻结与工程门禁

### 范围

对应 D01 至 D04、T13、T14。

### 实施内容

- 确认 Embedded-first 的产品范围。
- 确认 durable、Continue、Drain 和 Pi 兼容策略。
- 建立规范 conformance matrix，记录每条 MUST 对应的测试。
- 建立 CI，执行格式检查、`go vet`、`go test -race` 和模块级测试。
- 配置 `golangci-lint`，先禁止新增告警，再逐步清理存量告警。
- 将 M0 至 M13 的历史状态压缩为本文件，不再维护重复的进度文档。
- 更新 T13 中已经确认的文档漂移。

### 产出

```text
CI workflow
Conformance matrix
D01-D04 决策记录
T13 文档清理清单
T14 lint 基线
```

### 退出条件

```text
CI 能执行 gofmt、go vet、go test -race
每条规范 MUST 都有测试或 Future 状态
D01-D04 有明确结论
旧 M0-M13 文档不再作为实施计划来源
```

## 阶段一：P0 语义与数据安全

### 范围

对应 T01、T02、T03。

### 实施内容

#### T01：统一 ID 生成

- 引入可注入的 `IDGenerator`。
- 新生成的 ConversationID、AgentID、RunID 和 SpawnID 使用随机 ID 或 ULID。
- 保持旧 snapshot ID 的读取兼容。
- 删除依赖进程内序号的跨进程身份生成逻辑。
- 让两个同时运行的 Orchestrator 也不会生成重复 ID。

#### T02：修复空消息判定

- 增加统一的消息内容判定函数。
- 空字符串、纯空白文本和空文本 Part 都视为无内容。
- 有效的图片、文件和 JSON Part 仍然可以作为消息内容。
- Prompt、Steer 和 FollowUp 使用同一套校验。

#### T03：打通 Continue

- Orchestrator 增加 Continue 分派路径。
- Host 根据 `Command.Continue` 调用 `ControllableAgent.Continue`。
- Continue 不追加 user Message，不合成空 Prompt。
- Agent 不支持 Continue 时返回 `not_supported`。
- HTTP 增加 Continue 输入，或者明确返回不支持，不能静默降级。
- gRPC、HTTP 和 Embedded 使用同一套 Continue 语义。

### 正式测试

```text
Restore 后新建 Conversation 不覆盖旧记录
两个 Orchestrator 并发生成 ID 不重复
空 Prompt、空 Steer、空 FollowUp 被拒绝
Continue 不增加 user message
不支持 Continue 时返回 not_supported
gRPC、HTTP、Embedded 的 Continue 行为一致
```

### 退出条件

```text
旧 Conversation 的 byID 与 byKey 在 Restore 后保持一致
Continue 通过所有协议时 transcript 不增加 user Message
阶段一相关测试在 go test -race 下通过
M12 与 M13 的完成状态重新核对
```

## 阶段二：并发、事件与生命周期

### 范围

对应 T04、T05、T06、T08、T09、T10。

### 实施内容

#### 限制配置

- 导出 `DefaultLimits`。
- 保持“显式传入 CoreLimits 时整体覆盖”的语义。
- 明确表达“使用默认值”和“显式禁用”的区别。

#### 事件订阅

- 每个订阅拥有独立的关闭信号。
- `Subscribe` 的等待 goroutine 同时监听 Context 和订阅关闭信号。
- Host 的订阅绑定请求 Context，不再使用永久存活的 Background Context。
- Agent 关闭时回收所有订阅。

#### FIFO Stream

- Stream 请求使用 admission 后的 RunID 建立事件归属。
- 排队期间不读取前序 Run 的事件。
- 只转发当前 Run 的事件。
- Run 结束、取消或投递失败时分别结束对应状态。

#### Idle 与进度

- `WaitForIdle` 按“取得通知通道、检查状态、等待”的顺序实现。
- 为状态切换增加可控测试钩子，避免依赖随机调度。
- 使用 atomic 或 mutex 保护 ToolProgress 的计数和字节数。
- 明确 Progress 回调的跨 goroutine 调用规则。

#### Drain

- 引入显式 DrainPolicy。
- 默认策略继续等待并报告未完成条目。
- 命令行服务可以开启 `AbortAfterGrace`。
- GracePeriod 后被取消的 Run 必须进入正常结算和保存路径。

### 正式测试

```text
WithLimits 部分字段覆盖后仍使用默认限制
1000 次 Subscribe + Close 后 goroutine 回到基线
FIFO 下多个 Stream 只收到自己的 Run 事件
WaitForIdle 在人为放大的切换窗口中不会超时
并发 Progress 回调在 -race 下通过
Drain 的等待和 AbortAfterGrace 两种策略都能验收
```

### 退出条件

```text
没有订阅 goroutine 持续增长
FIFO Stream 不再串 Run
所有并发测试在 go test -race 下通过
Drain 不会把 Busy 或 Closing Agent 虚报为已关闭
```

## 阶段三：长会话与持久化

### 范围

对应 T07、A03、A04，并落实 D01。

### 实施内容

#### Transcript 性能

- 维护累计 transcript 字节数。
- 提交新消息时只计算增量。
- 避免每次 commit 都深拷贝并 Marshal 全量 transcript。
- 增加 100、1000 和 10000 条消息的 benchmark。

#### Compaction

- 增加 `Compactor` 能力。
- Compaction 在 Core Loop 内作为提交态操作执行。
- 压缩历史前缀并写入摘要消息。
- 发出独立的 `transcript_compacted` 事件。
- 未配置 Compactor 时保留原有 `limit_exceeded` 行为。

#### Run checkpoint

- 每个 Run 在结算边界保存 checkpoint。
- checkpoint 使用已有 StateVersion CAS。
- Restore 时区分 Conversation snapshot 与 Run checkpoint。
- 文档明确进行中 Run 的恢复边界。

### 暂不实现

```text
任意 Tool 中间状态的 replay
跨进程 Agent placement
多 Pod 事件续传
完整工作流重放引擎
```

### 正式测试

```text
超过 MaxTranscriptBytes 的会话经过 Compaction 后仍可继续
Compaction 后 snapshot 可以 Restore
Run 结算后的 checkpoint 可以在重启后恢复
Store 失败不会报告虚假的成功
```

### 退出条件

```text
长会话不会因 transcript 单调增长而永久失效
durable 的文档定义与实际恢复能力一致
性能 benchmark 证明 commit 不再按历史长度平方级增长
```

## 阶段四：Core 契约与模块边界

### 范围

对应 A01、A02、A05、A06、T15。

### 实施内容

#### Agent 能力边界

保留最小 `Agent` 接口，为 Orchestration 定义明确的管理能力集合。建议将 `Snapshotter`、`IdleWaiter`、`EventSource`、`RunCanceler` 和 `AgentLifecycle` 统一为 `ManagedAgent`。注册时检查能力，不把缺能力延迟到 Retire 或 Stream 时才失败。

#### Provider 数据隔离

- 为 Message 和 ToolCall 增加不透明的 ProviderData。
- Codex item id 不再拼入 ToolCall.ID。
- 完成 Message.Usage 和 ModelOptions 的传递。
- 清理从未赋值或没有消费方的 ToolUse 字段。
- 修复 integer Schema 校验边界。

#### Event Contract

- 为每种 EventKind 固定 payload 字段、类型和必填性。
- 第一版保留现有 wire 形状，先用构造函数和测试锁住 schema。
- ContractVersion 升级时再评估 typed payload 或 protobuf oneof。
- 增加 reasoning update 和 transcript compacted 的事件语义。

#### Core 拆包

拆包顺序固定为:

```text
testkit
  ↓
toolkit：typed function tool 与 Schema
  ↓
toolset：ToolSet 与激活逻辑
  ↓
根包清理
```

拆包期间不改变 Agent Loop、Event 顺序和错误分类。

### 退出条件

```text
Orchestration 的能力检查集中在注册期
Provider 私有信息不再污染 Core 身份字段
Event payload 具备可测试的 schema
root、host、orchestration 共用 testkit
拆包后 Embedded 调用方不需要增加无关依赖
```

## 阶段五：Host、协议与生态

### 范围

对应 A07、A08、T11、T12 和 D04。

### 实施内容

#### Host 分层

- `host` 只保留协议无关的 Service、生命周期和运行管理。
- HTTP handler 移到 HTTP adapter。
- async、poll、SSE 作为协议能力保留在 adapter 层。
- gRPC 与 HTTP 共用同一个 Service。

#### 请求身份

为 Host 请求补充:

```text
RequestID
CallerNamespace
IdempotencyKey
```

Conversation 路由键使用 namespace、AgentName 和 ConversationKey，避免不同调用方互相访问同一会话。

#### Extension 调度

- EventObserver 默认使用异步或 advisory 包装器。
- 同步 observer 必须显式选择。
- 所有异步 Extension 都有 Context、关闭信号和错误处理。
- I/O 型 Extension 不得无限期阻塞 Agent Loop。

#### 安全与日志

- 管理端点增加 middleware 注入点。
- README 明确无鉴权参考服务只适用于本地或受信网络。
- 实现 request log，或删除空的 requestLog 中间件。

#### Pi 与 Codex

- 默认不再修改 Pi 的 `auth.json`。
- 凭据放到 Gotato 自有配置目录。
- 不伪装 Pi 的 originator。
- 如果继续提供 Pi 兼容能力，独立为 `pi-compat` 包并标为实验性。
- 为 token refresh 和凭据落盘增加临时目录测试。

#### 生态补齐

按以下顺序交付:

```text
MCP ToolSet
  ↓
Anthropic Model adapter
  ↓
testkit 对外稳定
  ↓
完整嵌入式 Go 服务示例
```

暂不引入完整 Graph、RAG、评测框架和工作流平台能力，避免项目变成半个通用 Agent Framework。

### 退出条件

```text
HTTP 与 gRPC 共用 Host Service
Conversation 路由具备 namespace
管理端点有明确安全边界
Codex refresh 与落盘路径有测试
MCP ToolSet 有端到端示例
至少一个已有 HTTP 服务可以嵌入 Gotato
```

## 阶段六：条件性 Hosted 多进程

### 进入条件

只有阶段五退出评审确认以下事实后才进入:

- 单进程路由已经成为实际容量限制。
- 用户需要跨 Pod 保持 Conversation 连续性。
- 用户需要故障转移或跨进程 Agent placement。

### 实施内容

对应 A10，需要单独立项实现:

```text
外置路由表
  ↓
Conversation lease
  ↓
分布式 CAS 与 generation fencing
  ↓
按 Conversation 分片的队列
  ↓
事件持久化与重连
  ↓
故障转移与恢复
```

`CancelRun` 和 `CloseAgent` 的索引优化可以在阶段五提前完成，但不把它误称为多进程支持。

### 退出条件

```text
两个进程不会同时持有同一 Conversation 的有效 lease
旧 generation 无法提交新状态
进程退出后 Conversation 可以被其他实例接管
客户端可以按 RunID 重连事件流
```

## 5. 里程碑与验收

| 里程碑 | 对应阶段 | 交付结果 | 发布门槛 |
| --- | --- | --- | --- |
| R0 | 阶段零 | CI、规范矩阵、定位决策 | 基础命令自动执行 |
| R1 | 阶段一 | ID、消息校验、Continue | 无数据覆盖、无空 Prompt |
| R2 | 阶段二 | 订阅、FIFO、Drain、并发安全 | `go test -race ./...` 通过 |
| R3 | 阶段三 | 增量提交、Compaction、checkpoint | 长会话可继续并可恢复 |
| R4 | 阶段四 | ManagedAgent、ProviderData、Event schema、testkit | 公共 API 有迁移说明 |
| R5 | 阶段五 | Host 分层、MCP、Provider、示例 | Embedded Beta 可运行 |
| R6 | 阶段六 | 多进程 Hosted | 仅在真实需求触发后验收 |

## 6. 推荐 PR 顺序

```text
PR 1  CI、lint 基线、Conformance Matrix
PR 2  IDGenerator 与 Restore 防撞
PR 3  空消息校验与 Continue 全链路
PR 4  WithLimits、WaitForIdle、ToolProgress
PR 5  Event subscription 生命周期与 goroutine 回收
PR 6  FIFO Stream admission 与 RunID 过滤
PR 7  DrainPolicy 与取消后的 checkpoint
PR 8  Transcript 增量计数与 benchmark
PR 9  Compactor 与 transcript_compacted event
PR 10 Run checkpoint 与持久化语义修订
PR 11 ManagedAgent、ProviderData、Usage、ModelOptions
PR 12 Event payload schema 与 reasoning event
PR 13 testkit、toolkit、toolset 拆分
PR 14 Host 与 HTTP adapter 拆分
PR 15 MCP、Anthropic、Pi 解耦与真实集成示例
PR 16 Hosted 多进程设计，仅在阶段六条件满足时开始
```

每个 PR 都必须满足:

```text
代码改动
  ↓
正式测试
  ↓
go test -race
  ↓
TODO.md 状态更新
  ↓
Conformance Matrix 更新
```

## 7. 风险与回退

| 风险 | 影响 | 缓解措施 | 回退方式 |
| --- | --- | --- | --- |
| ID 格式变化 | 旧 snapshot 无法读取 | 保留旧格式读取兼容 | 暂停新格式生成，保留兼容解析 |
| Continue 契约变化 | HTTP、gRPC 行为不一致 | 共用 Service 与跨协议测试 | 暂时对不支持协议返回 `not_supported` |
| Event schema 变化 | 客户端解析失败 | ContractVersion 和兼容字段 | 保留旧 payload，延后 typed payload |
| Compaction 摘要错误 | 会话上下文丢失 | 摘要前保存 snapshot，增加回滚版本 | 关闭自动 Compaction，恢复 limit_exceeded |
| Drain 强制取消 | Run 结果改变 | AbortAfterGrace 作为显式开关 | 默认关闭强制取消 |
| Host 拆包 | import 路径变化 | 保留过渡 re-export | 先只拆内部实现，保持公开入口 |
| Hosted 范围膨胀 | 工期和复杂度失控 | 阶段六必须有真实需求触发 | 保持 Embedded-first |

## 8. 最终完成定义

Gotato 达到 Embedded Runtime v1 需要满足:

- Conversation、Agent 和 Run 的身份在重启后不会冲突。
- Continue、Steer、FollowUp 和 Abort 的语义在 Core、Host 和协议层一致。
- 事件订阅不会泄漏 goroutine，FIFO Stream 不会串 Run。
- Transcript 支持增量提交，长会话具备 Compaction 路径。
- 已结算 Conversation 可以通过 checkpoint 恢复。
- Orchestration 的管理能力在注册期可检查。
- Provider 私有状态不污染 Core 模型。
- Event payload 有明确 schema 和版本。
- Host 可以被替换，HTTP 与 gRPC 不实现两套 Agent 语义。
- CI、race test、lint 和 Conformance Matrix 成为合并门禁。
- 至少一个真实 Go 服务完成嵌入式集成。

## 9. 当前行动项

| 顺序 | 行动 | 对应 TODO | 完成标准 |
| --- | --- | --- | --- |
| 1 | 拍板 D01 至 D04 | D01-D04 | 决策记录完成 |
| 2 | 建立 CI 与规范矩阵 | T13、T14、A09 | CI 能阻止新增回归 |
| 3 | 修复身份与 Continue | T01、T02、T03 | 阶段一退出条件全部满足 |
| 4 | 修复订阅与 FIFO | T05、T06 | race 与串流测试通过 |
| 5 | 实现 Compaction 与 checkpoint | T07、A03、A04 | 长会话可继续并恢复 |
| 6 | 稳定 Core 公共契约 | A01、A05、A06、A02 | testkit 与 schema 稳定 |
| 7 | 补 MCP 与集成示例 | D04 | Embedded Beta 可运行 |
| 8 | 复评 Hosted 必要性 | D02、A10 | 满足条件才启动阶段六 |

## Changelog

| 日期 | 版本 | 变更 |
| --- | --- | --- |
| 2026-09-02 | 1.0 | 用分阶段改造计划替换 M0 至 M13 历史进度文档 |
