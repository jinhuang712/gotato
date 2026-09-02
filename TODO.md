# Gotato TODO

| 项 | 值 |
| --- | --- |
| 基线日期 | 2026-09-02 |
| 来源 | 全仓库代码审阅,配合临时探针测试验证。探针已删除,未入库 |
| 用法 | 一个问题一个 section。修完勾掉复选框,并把「退出条件」变成仓库里的正式测试 |
| 分级 | P0 破坏已宣称完成的里程碑;P1 第一个真实接入方就会撞上;P2 质量与一致性 |
| 编号 | T 缺陷;A 架构与设计张力;D 需要拍板的定位决策 |

代码 pin 形如 `文件:行号`,以基线日期的 `main` 分支为准,改动后请更新。

三类条目的性质不同。T 类有明确的错误行为和退出条件,修就是了。A 类是设计选择带来的结构性张力,每条先要一个决策再要一个实现。D 类是项目定位,决定 A 类里哪些值得做。建议顺序:先拍 D,再排 A,T 类里 P0 与 P1 不受 D 影响,随时可做。

## 状态总表

| 编号 | 问题 | 级别 | 状态 |
| --- | --- | --- | --- |
| [T01](#t01-三种-id-用进程内计数器重启后撞车) | 三种 ID 用进程内计数器,重启后撞车 | P0 | 未开始 |
| [T02](#t02-core-空消息校验放过空文本-part) | Core 空消息校验放过空文本 Part | P0 | 未开始 |
| [T03](#t03-hostorchestration-的-continue-退化为空-prompt) | Host/Orchestration 的 Continue 退化为空 Prompt | P0 | 未开始 |
| [T04](#t04-部分填写的-withlimits-让-agent-不可用) | 部分填写的 WithLimits 让 Agent 不可用 | P1 | 未开始 |
| [T05](#t05-每次事件订阅泄漏一个-goroutine) | 每次事件订阅泄漏一个 goroutine | P1 | 未开始 |
| [T06](#t06-stream-路径不按-runid-过滤fifo-下串事件) | Stream 路径不按 RunID 过滤,FIFO 下串事件 | P1 | 未开始 |
| [T07](#t07-commitmessage-每次提交重新序列化整份-transcript) | commitMessage 每次提交重新序列化整份 transcript | P1 | 未开始 |
| [T08](#t08-drain-超时不-abort进程退出丢失会话状态) | drain 超时不 Abort,进程退出丢失会话状态 | P1 | 未开始 |
| [T09](#t09-waitforidle-存在丢失唤醒的窗口) | WaitForIdle 存在丢失唤醒的窗口 | P2 | 未开始 |
| [T10](#t10-toolprogress-计数器无同步保护) | ToolProgress 计数器无同步保护 | P2 | 未开始 |
| [T11](#t11-codex-适配器的外部风险与缺测) | Codex 适配器的外部风险与缺测 | P2 | 未开始 |
| [T12](#t12-管理端点无鉴权请求日志中间件是空实现) | 管理端点无鉴权,请求日志中间件是空实现 | P2 | 未开始 |
| [T13](#t13-文档与代码漂移清单) | 文档与代码漂移清单 | P2 | 未开始 |
| [T14](#t14-工程卫生cilicenselint) | 工程卫生:CI、LICENSE、lint | P2 | 未开始 |
| [T15](#t15-小项) | 小项 | P2 | 未开始 |

### 架构与设计张力

| 编号 | 问题 | 依赖决策 | 状态 |
| --- | --- | --- | --- |
| [A01](#a01-最小-agent-接口是名义上的) | 最小 Agent 接口是名义上的 | 无 | 待决策 |
| [A02](#a02-core-并不-tight) | Core 并不 tight | 无 | 待决策 |
| [A03](#a03-没有压缩长会话必死) | 没有压缩,长会话必死 | D01 | 待决策 |
| [A04](#a04-持久化只在退休时发生) | 持久化只在退休时发生 | D01 | 待决策 |
| [A05](#a05-provider-表示泄进了-core-身份字段) | Provider 表示泄进了 Core 身份字段 | 无 | 待决策 |
| [A06](#a06-event-payload-没有-schema) | Event payload 没有 schema | 无 | 待决策 |
| [A07](#a07-host-层混杂且最不差异化) | Host 层混杂且最不差异化 | D02 | 待决策 |
| [A08](#a08-extension-默认阻塞并在-agent-goroutine-内运行) | Extension 默认阻塞并在 Agent goroutine 内运行 | 无 | 待决策 |
| [A09](#a09-规范强度高于实现成熟度) | 规范强度高于实现成熟度 | 无 | 待决策 |
| [A10](#a10-orchestrator-单锁单进程) | Orchestrator 单锁单进程 | D02 | 待决策 |

### 定位决策

| 编号 | 决策 | 建议 | 状态 |
| --- | --- | --- | --- |
| [D01](#d01-产品论点的兑现顺序) | 产品论点的兑现顺序 | 先压缩与提交日志,再广度 | 待拍板 |
| [D02](#d02-embedded-还是-hosted) | Embedded 还是 Hosted | 收紧到 Embedded,Host 作参考部署 | 待拍板 |
| [D03](#d03-与-pi-的关系) | 与 Pi 的关系 | 二选一,不要悬着 | 待拍板 |
| [D04](#d04-广度补哪些) | 广度补哪些 | MCP ToolSet、Anthropic 适配器、testkit、一个真实集成示例 | 待拍板 |

---

## T01 三种 ID 用进程内计数器,重启后撞车

- [ ] 级别:P0。直接违背 M12 宣称的「ConversationID 跨重启稳定」

### 现象

`Orchestrator.Restore` 把磁盘上的 Conversation 装回路由表,但不推进 ID 计数器。重启后第一个新建的 Conversation 又拿到 `conversation-1`,覆盖 `byID` 里刚恢复的那条。被覆盖的 Conversation 在 `byKey` 里仍指向旧 entry,成为孤儿:按 key 能解析到它,按 ID 查到的却是另一个会话,Retire 它的 ID 会退休别人。

RunID 与 AgentID 同样是进程内计数器。重启后 `GET /v1/runs/{run_id}` 的 poll 表和 `CloseAgent` 都会串号。

### 证据

- `orchestration/orchestrator.go:526`:`nextID("conversation", &o.seq)`
- `orchestration/orchestrator.go:1179`:计数器实现
- `orchestration/orchestrator.go:1215`:`Restore` 不触碰 `o.seq`
- `types.go:218`:Core 的 `nextID` 同样是全局计数器,产出 `agent-N`、`run-N`、`message-N`

### 复现

```text
1. 进程 1:Dispatch key=alpha,Retire(Retain) → 磁盘上有 conversation-1
2. 进程 2:同一目录 Restore → byID[conversation-1] = alpha(dormant)
3. 进程 2:Resolve key=beta → 新 ID 也是 conversation-1,byID 被覆盖
4. Get(conversation-1) 返回 beta;Resolve key=alpha 仍能复活,但两条记录共用一个 ID
```

探针输出摘录:

```text
new conversation in process 2: id=conversation-1 key=beta
COLLISION: restored key "alpha" now resolves to {ID:conversation-1 Key:beta ... Status:active}
```

### 修法

ConversationID、AgentID、RunID 全部改为随机 ID(crypto/rand 编码或 ULID),不要靠计数器。MessageID 仅在 transcript 内部使用,可以保留计数器但建议一并统一。`SpawnID` 同理。

不要用「Restore 时从已恢复的 ID 里解析最大序号」修补:多进程共享同一个 store 目录时仍会撞。

### 退出条件

```text
Restore 之后新建 Conversation,已恢复的记录 byID / byKey 都不受影响
两个 Orchestrator 共享同一 store 目录先后启动,ID 不重复
RunID 在进程重启前后不重复
测试用固定种子或注入 ID 生成器保证确定性(specs/11 §2 要求 deterministic ID generation)
```

---

## T02 Core 空消息校验放过空文本 Part

- [ ] 级别:P0。它是 T03 能「成功」的原因,也让任何调用方都能提交空 user 消息

### 现象

`UserMessage("")` 与 `UserMessage("   ")` 都能通过 Prompt 与 Steer 的校验并被提交进 transcript。

### 证据

- `agent.go:1208` `validatePrompt`:条件是 `TrimSpace(TextOf(m)) == "" && len(m.Parts) == 0`。一个空文本 Part 让 `len(Parts)` 为 1,于是「空」判定失败
- `agent.go:1218` `validateControlMessage`:同一写法

探针输出:

```text
Prompt(UserMessage("")) => err=<nil>
Prompt(UserMessage("   ")) => err=<nil>
Steer(UserMessage("")) => err=<nil>
transcript length after two empty prompts = 4
```

### 修法

「非空」应定义为:至少有一个 Part 带有实质内容。文本 Part 要求 TrimSpace 后非空;image / json Part 要求 Data 或 Text 非空。把这个判定抽成一个函数,Prompt、Steer、FollowUp 共用。

### 退出条件

```text
UserMessage("") 与纯空白文本被 Prompt / Steer / FollowUp 拒绝,错误码 invalid_argument
只有 image Part 且 Data 非空的消息仍被接受
```

---

## T03 Host/Orchestration 的 Continue 退化为空 Prompt

- [ ] 级别:P0。M13 宣称冻结的 ContractVersion "1" 里,Continue 的语义是错的

### 现象

`host.Command.Continue = true` 经 `Service.Run` / `Service.StreamRun` 后,实际执行的是一次带空 user 消息的 Prompt。transcript 多出一条 `role=user text=""`。specs/03 §2 规定 Continue「appends no user Message」。gRPC 的 `ContinueInput` 走同一条路,同样受影响。HTTP 的 `runRequest` 根本没有 continue 字段。

### 证据

- `host/service.go:147`:`Run` 无条件调用 `Dispatch(..., gotato.UserMessage(command.Prompt))`
- `host/service.go:182`:`StreamRun` 同上
- `orchestration/orchestrator.go` 的 `dispatch` 只调用 `agent.Prompt`,没有 Continue 分派路径
- `adapter/grpc/server.go` `commandOf`:`ContinueInput` 映射为 `Command.Continue = true`

探针输出:

```text
continue => status=completed err=<nil>
transcript before continue = 4 messages, after = 6
  [4] role=user text="" parts=1
```

### 修法

1. Orchestrator 增加 Continue 分派:在同一 lease / 单飞 / 容量路径下调用 `ControllableAgent.Continue`,Agent 不实现该接口时返回 not_supported
2. Host 的 `Run` / `StreamRun` 按 `Command.Continue` 分派到对应路径
3. HTTP 的 `runRequest` 补 `continue` 字段,或在文档里明确 HTTP 不支持 Continue
4. 修 T02 之后,当前这条路径会从「静默错误」变成「invalid_argument」,那是过渡态,不是修复

### 退出条件

```text
Host Continue 之后 transcript 不新增 user 消息,且 Model 收到的上下文以 tool_result 或 user 结尾
transcript 以 assistant 结尾时 Host Continue 返回 invalid_state
gRPC ContinueInput 与 HTTP 的 Continue 行为一致
等价性测试覆盖 Continue:Embedded 与 Hosted 事件序列相同
```

---

## T04 部分填写的 WithLimits 让 Agent 不可用

- [ ] 级别:P1

### 现象

`gotato.WithLimits(gotato.CoreLimits{MaxTurns: 10})` 之后每次 Prompt 都失败:`limit_exceeded (commitMessage): maximum Messages exceeded`。

### 证据

- `agent.go:140` `WithLimits`:一旦调用就把 `limitsSet` 置真
- `agent.go:1416` `limitExceededUint32`:`explicit && limit == 0` 视为「不允许任何工作」
- 这符合 specs/08 §5 的字面(explicitly configured zero count/byte limits admit no work),但没有任何公开方式表达「其余字段用默认值」,`defaultLimits()` 是未导出的

### 修法

任选其一,并在文档里写清零值语义:

- 导出 `DefaultLimits()`,让调用方在默认值上改字段;`WithLimits` 保持整体覆盖语义
- 或把「显式禁用」改成指针字段 / 显式 sentinel,零值恒为「用默认值」

第一种改动最小,也与 specs/08 兼容。

### 退出条件

```text
WithLimits(CoreLimits{MaxTurns: 10}) 之后 Prompt 正常完成,其余上限取默认值
「禁用某个上限」有一种明确、有测试的写法
```

---

## T05 每次事件订阅泄漏一个 goroutine

- [ ] 级别:P1

### 现象

`Subscribe(ctx)` 为每个订阅启动一个 goroutine 等待 `ctx.Done()`。订阅的 `Close()` 与 hub 的 `close()` 都不会结束它。用 `context.Background()` 订阅时它永不退出。

### 证据

- `events.go:123`:eventHub 的 `go func() { <-ctx.Done(); ... }`
- `events.go:256`:lifecycleHub 同样写法
- `host/async_runs.go:170`:`source.Subscribe(context.Background())`。每个 `/v1/runs/async` 与 `/v1/runs/progress` 请求在 Agent 生命期内泄漏一个 goroutine

探针输出:

```text
goroutines before=3 after 1000 subscribe+close=1003 (delta=1000)
```

### 修法

订阅结构体加一个 `done chan struct{}`,`closeWith` 关闭它;等待 goroutine 改为 `select { case <-ctx.Done(): ...; case <-done: }`。两个 hub 代码几乎相同,顺手用泛型合并成一个实现。

### 退出条件

```text
Subscribe + Close 一千次后 goroutine 数量回到基线(允许运行时噪声)
Agent Close 之后所有订阅相关 goroutine 退出
```

---

## T06 Stream 路径不按 RunID 过滤,FIFO 下串事件

- [ ] 级别:P1。默认 reject 策略下不触发,开了 `--queue fifo` 就会

### 现象

`runStream` 与 `Service.StreamRun` 先订阅 Agent 事件再 Dispatch。同一 Conversation 若有排在前面的 Run,订阅者会先收到那个 Run 的事件,在它的 `agent_end` 处停止读取并等待自己的结果;自己的 Run 事件被丢弃,客户端看到的是别人的 Run。

### 证据

- `host/server.go` `runStream`:循环里只看 `event.Kind == EventAgentEnd`,不比较 RunID
- `host/service.go:182` 起的 `StreamRun`:同样
- `host/async_runs.go` 的 `startAsyncRun` 用 `DispatchWithAdmission` 在拿到 lease 后才开始读事件,已经规避了这个问题

### 修法

Stream 路径统一改用 `DispatchWithAdmission`:admitted 之前的事件丢弃,admitted 之后以第一个 `agent_start` 的 RunID 为锚,只投递该 RunID 的事件,在它的 `agent_end` 结束。

### 退出条件

```text
QueueFIFO 下两个并发 stream 请求各自只收到自己 Run 的事件
Embedded / Hosted 等价性测试在 FIFO 配置下仍通过
```

---

## T07 commitMessage 每次提交重新序列化整份 transcript

- [ ] 级别:P1。性能问题,长会话会明显放大

### 现象

每次提交一条消息,都深拷贝全部历史并把整份 transcript 再 Marshal 一次来检查字节上限。提交次数乘以 transcript 大小,是 O(n²)。默认上限 32MB 意味着一个长会话里每条 tool result 都可能触发几十 MB 的序列化。

### 证据

- `agent.go:641` `commitMessage`
- `agent.go:657`:`transcript, err := json.Marshal(candidate)`
- 同一函数里 `cloneMessages(a.messages)` 每次深拷贝;`executeRun` 每个 Turn 再拷贝一次给 transformer

### 修法

维护一个累计的 transcript 字节数,提交时只 Marshal 新消息并累加;删除或压缩消息时同步扣减。`a.messages` 只在 Agent goroutine 内可变,追加时不需要为了不变性再整体拷贝。

### 退出条件

```text
提交一条消息的开销与 transcript 长度无关
基准测试:1000 条 4KB 消息的会话,提交耗时线性
```

---

## T08 drain 超时不 Abort,进程退出丢失会话状态

- [ ] 级别:P1

### 现象

`Close` 按 specs/16 §4 设计不取消进行中的 Run。`Drain` 因此对每个 Busy Agent 等待到 ctx 到期,报 `DrainIncomplete`。`cmd/gotato-agent` 收到 SIGTERM 后给 drain 10 秒,随后关 HTTP 并退出进程。进行中的 Run 既没结算也没落盘,那条 Conversation 自上次退休以来的全部状态丢失。

### 证据

- `orchestration/orchestrator.go` `Drain` → `Retire` → `waitEntryIdle(ctx)`,没有 Abort 分支
- `cmd/gotato-agent/main.go`:drain 10s,shutdown 5s,之后进程结束
- specs/16 §13 只要求「报告 incomplete drain」,没要求尽力保存

### 修法

给 Drain 一个显式策略,例如 `DrainPolicy{GracePeriod, AbortAfterGrace bool}`:超过 GracePeriod 后对仍 Busy 的 Agent 调 `Abort`,Run 以 cancelled 结算,再走正常 Retire 把 snapshot 落盘。这与 specs/16 §4「要停 Run 必须显式 Abort」一致。同时在 README 写明当前的丢失窗口。

### 退出条件

```text
带 AbortAfterGrace 的 Drain 在 GracePeriod 后把 Busy Agent 的 Run 取消并成功退休,store 里有它的 snapshot
默认策略保持现有行为,并有文档说明
```

---

## T09 WaitForIdle 存在丢失唤醒的窗口

- [ ] 级别:P2。代码层面成立,16000 次压测未复现,概率低

### 现象

`WaitForIdle` 先读 `Status()`,再取 `stateChange` channel。如果 `setStatus(Idle)` 恰好在两步之间执行,调用方拿到的是新 channel,会一直等到下一次状态变化。Retire 依赖 WaitForIdle,而 loop 是先送 result 再置 Idle,所以 Retire 路径上这个窗口是真实存在的,只是极窄。

### 证据

- `agent.go:482` `WaitForIdle`
- `agent.go` loop:`cmd.result <- ...; <-a.admission; a.setStatus(AgentIdle)`
- `orchestration/orchestrator.go:883`:Retire 调用 WaitForIdle

### 修法

先在锁内取 channel,再检查 Status;Status 已满足就直接返回。这是 check-then-wait 的标准写法。

### 退出条件

```text
代码顺序改为「取 channel → 检查状态 → 等待」
补一个用 runtime.Gosched 或注入钩子放大窗口的确定性测试
```

---

## T10 ToolProgress 计数器无同步保护

- [ ] 级别:P2

### 现象

`progressUpdates` 与 `progressBytes` 在闭包里裸读写。Tool 若从多个 goroutine 调用 progress,就是数据竞争。

### 证据

- `toolbatch.go:97` 附近的 progress 闭包

### 修法

要么用 atomic / mutex 保护,要么在 `ToolProgress` 的文档契约里写明「只能从 Execute 所在 goroutine 调用」,并在 race 测试里加一个多 goroutine 调用的 Tool 证明选择的那条成立。

### 退出条件

```text
多 goroutine 调用 progress 的 Tool 在 -race 下通过,或契约文档明确禁止且有说明
```

---

## T11 Codex 适配器的外部风险与缺测

- [ ] 级别:P2

### 现象

`gateway/codex.go` 硬编码了 Pi 的 OAuth client id,并把 `originator` 头设为 `pi`,等于以 Pi 客户端身份访问 ChatGPT 后端。它还会回写用户的 Pi `auth.json`。token refresh 与落盘路径没有测试,M3 已标记「凭据落盘补测」。

### 证据

- `gateway/codex.go`:`codexOAuthClientID`、`req.Header.Set("originator", "pi")`、`persistPiCredential`
- `gateway/codex_test.go`:四个用例都不覆盖 refresh 与 persist

### 修法

1. README 与 `gateway.codex.example.yaml` 明确告知:该模式复用 Pi 的凭据并会改写其文件,使用者自行承担对应服务条款风险
2. 回写改为可选(默认关闭),或写到 Gotato 自己的凭据文件
3. 用 httptest 假 token endpoint 覆盖 refresh 成功、失败、文件写回三条路径

### 退出条件

```text
refresh 与 persist 路径有测试
回写 Pi 文件是显式 opt-in
文档写明风险
```

---

## T12 管理端点无鉴权,请求日志中间件是空实现

- [ ] 级别:P2。本地参考实现可以接受,但要标清楚

### 现象

`/admin/drain` 与 `/v1/agents/{id}/close` 没有任何鉴权,监听地址可以通过 `--addr` 改成对外。`requestLog` 什么都不做。

### 证据

- `host/server.go` `Handler`:路由表
- `host/server.go:434` `requestLog`

### 修法

- README 标注:HTTP 参考服务无鉴权,仅限本地或受信网络
- `requestLog` 要么实现,要么删掉,不要留一个名不副实的中间件
- 给 Host 预留一个 `http.Handler` 中间件注入点,让接入方挂自己的鉴权与日志

### 退出条件

```text
文档有明确的安全边界声明
没有空实现的中间件
```

---

## T13 文档与代码漂移清单

- [ ] 级别:P2。逐项对齐,以代码或 progress 的决策为准

| 位置 | 文档说法 | 代码现状 | 处理 |
| --- | --- | --- | --- |
| `docs/03-core-runtime.md:39`、`specs/10-runtime-and-service-api.md:49` | `StreamingAgent` 接口 | 不存在,progress 已决定不做 | 删除 |
| `specs/08-errors-and-limits.md` §2、§5 | `agent_spawn_failure`、`MaxVisibleTools` | 不存在 | 删除或标 FUTURE |
| `specs/05-tools-and-toolsets.md` §11 | `WithToolSets` | 只有 `WithToolSet` / `WithActiveToolSet` | 补实现或改文档 |
| `specs/04-events-and-delivery.md` §1 | `routine_started` 等四个事件 | 未实现,M11/M13 推迟 | 在 specs 内标 FUTURE |
| `specs/09-agent-service-and-grpc.md` §3、§4 | 双向流、Steer/FollowUp 命令 | server-streaming,Steer/FollowUp 不在 Host | 标为草案或更新 |
| `specs/01`、`specs/02`、`specs/06` | `AgentState`、`Turn`、`ModelMessage` | 不存在,progress 待议 | 拍板后统一 |
| `agent.go:1280` 注释 | enum 不校验 | `validateSchemaValue` 校验了 enum | 改注释 |
| `toolfunc.go:115` 注释 | items 会校验 | 数组元素不递归校验 | 改注释或补实现 |
| `progress/README.md` 状态总表 | M12、M13「已完成」 | 受 T01、T03 影响 | 修完前改为「有已知缺陷」 |

### 退出条件

```text
每一行处理完毕,specs 里未实现的条目都带 FUTURE 标记
```

---

## T14 工程卫生:CI、LICENSE、lint

- [ ] 级别:P2

### 现象

没有 CI,progress 里的验收命令没有任何东西强制执行。没有 LICENSE,README 写「Not yet selected」。没有 golangci 配置,默认规则下 100 项告警。

| 类别 | 数量 | 说明 |
| --- | --- | --- |
| errcheck | 95 | 绝大多数是测试里 `defer agent.Close(...)` 未检查返回值 |
| unused | 4 | `model.go:53` `modelStreamDone`;三个测试结构体的未用字段 |
| staticcheck ST1005 | 1 | `host/async_runs.go:168` 错误串首字母大写 |
| gRPC 模块 errcheck | 3 | `adapter/grpc/server_test.go` |

### 修法

1. 加 GitHub Actions:两个模块分别跑 `gofmt -l`、`go vet`、`go test -race`、`golangci-lint`
2. 加 `.golangci.yml`,先把 errcheck 在 `_test.go` 里对 `Close` 放宽,把剩余告警清零
3. 删除 `modelStreamDone` 与未用字段
4. 选定 LICENSE
5. 加 Makefile 或 `mise`/`just` 任务把验收命令固化

### 退出条件

```text
PR 必须通过 CI 才能合并
golangci-lint 零告警
仓库有 LICENSE
```

---

## T15 小项

- [ ] 级别:P2。都已在代码里核实,单独做太碎,合并处理

| 项 | 位置 | 说明 |
| --- | --- | --- |
| `Message.Usage` 从不填充 | `agent.go` `readAssistant` / `executeRun` | usage 只累计到 RunResult,assistant 消息上的字段恒为零值。要么填,要么删字段 |
| `ToolUse.Executed`、`ToolUse.Result` 从不赋值 | `tool.go` | 两个字段没有写入点,删掉或说明用途 |
| `integer` 类型接受小数 | `agent.go` `schemaTypeMatches` | `"integer"` 与 `"number"` 同样只检查 float64,1.5 能通过 integer 校验 |
| reasoning delta 不产生 Event | `agent.go` `readAssistant` | 只有 text delta 发 `message_update`,流式 UI 看不到 thinking |
| `ModelOptions` 无法配置 | `model.go`、`agent.go` | `ModelRequest.Options` 存在但 Core 从不填,也没有 `WithModelOptions`;温度、max tokens、reasoning effort 都调不了 |
| 两个 `StateVersion` 含义不同 | `orchestration/orchestrator.go` `Record`、`types.go` `CoreSnapshot` | 一个是路由 CAS 版本,一个是 transcript 版本,同名易混,建议其一改名 |
| `Snapshot` 在 Busy 时阻塞而非返回 busy | `agent.go` `Snapshot` | 预检通过后发命令,若此时 Run 已开始,会等到 Run 结束。与注释「not idle → busy」不一致 |
| 测试用 sleep 断言「未发生」 | `host/lifecycle_test.go` `TestHTTPStreamDisconnectStopsDeliveryOnly` | `time.After(50ms)` 断言 Run 未被取消,specs/11 §10 要求不用 sleep |
| 事件 payload 是 `map[string]any` | `events.go`、`adapter/grpc` `payload_json` | 冻结的 wire contract 冻结了一个无 schema 的 payload,建议给每种 Event 定义类型化 payload |

---

# 架构与设计张力

这一部分不是 bug。每条都是一个设计选择带来的结构性后果,先要一个决策,再要一个实现。每条的「现状」都在代码里核实过。

## A01 最小 Agent 接口是名义上的

- [ ] 决策:聚合接口,还是承认 Orchestration 只接受 Core 具体实现

### 现状

`Agent` 只有 Prompt 与 Close 两个方法。但 Orchestration 实际依赖五个可选接口:`Snapshotter`、`IdleWaiter`、`AgentLifecycle`、`RunCanceler`、`EventSource`;Host 依赖 `EventSource`;全部靠类型断言在运行时发现。

- `orchestration/orchestrator.go` `Retire`:`agent.(gotato.IdleWaiter)`、`agent.(gotato.Snapshotter)`
- `orchestration/orchestrator.go` `CancelRun`:`agent.(gotato.RunCanceler)`
- `orchestration/orchestrator.go` `markRetirementFailed`、`pendingAgents`:`agent.(gotato.AgentLifecycle)`
- `host/service.go` `StreamRun`、`host/async_runs.go`:`agent.(gotato.EventSource)`

后果:一个只实现 `Agent` 的第三方实现无法被编排,也无法被 Host 流式投递;兼容性在编译期完全不可见。根包里的能力接口已有十来个。

### 可选方向

1. 定义 `OrchestratedAgent` 之类的聚合接口,列出 Orchestration 真正需要的全部方法,`Definition.New` 返回它。类型断言消失,第三方实现知道要满足什么
2. 承认 Orchestration 只接受 Core 的具体实现,`Definition.New` 返回 `*gotato.CoreAgent`,不再假装可替换
3. 保持现状,但在 `Register` 时逐个断言并拒绝缺能力的实现,把运行时失败提前到注册期

建议 1。它保留「Agent 契约可替换」的论点,又把要求写进编译器。

### 退出条件

```text
Orchestration 与 Host 代码里对 Agent 的类型断言归零,或全部集中在 Register
一个缺能力的 Agent 实现在 Register 或编译期被拒绝,而不是在 Retire 时
```

## A02 Core 并不 tight

- [ ] 决策:拆子包的边界

### 现状

根包约五千行,`agent.go` 近一千五百行。下面这些都在根包:

```text
ToolSet 与激活 Tool                toolset.go
typed func Tool 与 schema 生成       toolfunc.go
JSON Schema 子集校验器              agent.go 后半
六种 Extension 挂点                 extensions.go
控制命令 Steer / FollowUp / Continue / Abort
快照与生命周期事件
两个几乎相同的事件 hub              events.go
```

docs/00 §4 说 Core 只含「让 Agent 成为 Agent 的语义」。上面多数是能力,不是语义。三个包各自复制了一份 testModel,specs/11 §2 要求的 testkit 不存在。

### 可选方向

```text
gotato/            Agent · Message · Model · Tool · Event · Loop · Limits · 错误
gotato/toolkit/    NewFuncTool · schema 生成 · schema 校验
gotato/toolset/    ToolSet · 激活 Tool
gotato/testkit/    scripted Model · gated Model · Event 录制器 · 固定 ID 生成器
```

Extension 接口与控制命令留在根包:它们是 Loop 的挂点,拆出去会形成循环依赖。

### 退出条件

```text
根包只剩契约与 Loop,行数明显下降
三个包的测试夹具收成 testkit,一处维护
```

## A03 没有压缩,长会话必死

- [ ] 决策:Compact 由谁触发、以什么形态进入 transcript
- [ ] 依赖 D01

### 现状

产品论点是 Conversation 可以跨 Agent 生命、跨重启存活。但提交态 transcript 只增不减:`MaxTranscriptBytes` 默认 32MB,触到之后每个 Run 都以 limit_exceeded 结算,这条 Conversation 从此不可用。`ContextTransformer` 只裁剪发给 Model 的视图,不改变提交态,所以它减不了状态。specs/08 §5 写了「Core may invoke an explicitly configured compaction policy before rejecting a commitment」,代码没有对应物。Pi 内建 compaction。

### 可选方向

1. 一个 `Compact` 控制命令:由 Agent goroutine 在 Idle 时执行,调用一个 `Compactor` Extension 产出摘要消息,替换一段前缀,`stateVersion` 递增,发一个 `transcript_compacted` Protected Event
2. 在 commit 即将超限时自动触发 1,前提是装了 Compactor;没装就照旧失败
3. 只做 Reset,清空 transcript 保留 instruction。最便宜,但等于放弃「跨生命的 Conversation」

建议 1 加 2。压缩必须是提交态操作,并且只有 Agent goroutine 有权限做,这与 docs/00 §2 一致。

### 退出条件

```text
一个装了 Compactor 的 Agent 跑到超过 MaxTranscriptBytes 的会话仍能继续
压缩后的 snapshot 能复活,且 Model 看到摘要
未装 Compactor 时行为不变
```

## A04 持久化只在退休时发生

- [ ] 决策:是否要 Run 级崩溃恢复
- [ ] 依赖 D01

### 现状

活跃 Agent 的状态只在内存。快照只在 `Retire` 时写入 store,崩溃丢掉自上次退休以来的一切 Run。M12 的标题「持久化与重启恢复」与 README 的 durable 措辞超过了实现:能恢复的只有已经 Dormant 的 Conversation。

### 可选方向

1. append-only 提交日志:`commitMessage` 是唯一提交点,每次提交追加一行到 store,Restore 时用「最后一次快照 + 之后的日志」重建。成本低,换来 Run 级恢复
2. 每个 Run 结算时写一次快照。粒度粗,但实现只是把 Retire 里的 Snapshot + Save 搬到 lease 释放处
3. 保持现状,文档改口:「dormant Conversation 跨重启」,不用 durable

建议 2 作为第一步,1 作为后续。无论选哪个,都先改文档措辞。

### 退出条件

```text
文档中 durable 的含义与实现一致
选 1 或 2 后:kill -9 一个刚结算 Run 的进程,重启后该 Run 的消息在 transcript 里
```

## A05 Provider 表示泄进了 Core 身份字段

- [ ] 决策:给 ToolCall 与 Message 加不透明 provider 位

### 现状

- `gateway/codex.go` `emitCodexCall`:把 Codex 的 item id 用竖线拼进 `ToolCall.ID`,形如 `call_1|fc_1`;`splitCodexCallID` 再拆回来。provider 私有信息进了 Core 的身份字段,并随 transcript 持久化
- `types.go` `Message`:没有 metadata 位;`ContentJSON` 用字符串承载;`Usage` 字段无人填(见 T15)
- `model.go` `ModelOptions`:Core 从不填,没有 `WithModelOptions`(见 T15)

`ContentPart.Signature` 已经是正确的模式:Core 携带、不解释。

### 可选方向

给 `ToolCall` 与 `Message` 各加一个 `Provider map[string]string` 或 `[]byte` 的不透明位,由适配器读写,Core 只负责 Clone 与序列化。Codex 的 item id 搬进去,`ToolCall.ID` 回到只装 call_id。

### 退出条件

```text
ToolCall.ID 不再包含分隔符与 provider 私有 id
Codex 往返测试仍通过,快照复活后 replay 仍带正确 item id
```

## A06 Event payload 没有 schema

- [ ] 决策:是否在 ContractVersion 1 内类型化 payload

### 现状

`Event.Payload` 是 `map[string]any`,wire 上是 `payload_json`。M13 冻结的 contract 冻结了一个无 schema 的字段。`turn_end` 的 summary 也是嵌套 map。reasoning delta 不产生任何 Event,流式 UI 看不到 thinking。对一个以 canonical Events 为卖点的 runtime,这是最薄弱的一处。

### 可选方向

1. 每种 EventKind 定义一个 payload 结构体,`Event.Payload` 改为接口或联合体,wire 层用 oneof;这是 breaking change,要升 ContractVersion
2. 保持 map,但在 specs/04 里为每种 kind 写死 payload 的键与类型,并加测试锁住;wire contract 事实上有 schema,只是没进类型系统
3. 增加 `reasoning_update` Coalescable Event,与 payload 形态无关,可以先做

建议 3 立刻做,2 作为 ContractVersion 1 的补丁,1 留给 2。

### 退出条件

```text
每种 EventKind 的 payload 形态有文档与测试
reasoning delta 可通过 Event 观察
```

## A07 Host 层混杂且最不差异化

- [ ] 决策:Host 保留什么、删什么
- [ ] 依赖 D02

### 现状

`host.Server` 同时是 HTTP handler、`Service` 实现和 async run 表。五种执行方式:同步、async 加 poll、progress ndjson、SSE、gRPC。没有幂等键、请求 ID、租户命名空间。`routeKey` 只按 AgentName 加 key 分区,docs 与 specs 里的「caller namespace」代码里不存在。`requestLog` 是空实现(见 T12)。

Host 是三层里最不差异化的一层:任何 Go 团队都会用自己已有的 HTTP 框架、鉴权与日志。它现在却是代码最多样的一层。

### 可选方向

1. `Service` 接口留在 host 包;HTTP handler 与 async run 表搬到 `adapter/http`,与 `adapter/grpc` 对称
2. 五种执行方式收成两种:同步 Run 与 StreamRun。async 与 progress 作为 HTTP adapter 内部的便利,不进 `Service`
3. `Request` 加 `Namespace`,`routeKey` 加进去。这是最小的多租户前提
4. 不做多进程路由,直到 D02 决定要做 Hosted

### 退出条件

```text
host 包只剩 Service 与投影类型
两个协议适配器形态对称
ConversationKey 有命名空间前缀
```

## A08 Extension 默认阻塞并在 Agent goroutine 内运行

- [ ] 决策:默认值是否改为 advisory,或加超时

### 现状

`EventObserver` 在每个 Event 边界同步等待,`extensions.go` `observe`;一个做 I/O 的观察者会卡住 Loop。所有六种挂点默认 blocking,`AdvisoryExtension` 是 opt-in。这符合 specs/06,但默认值偏危险:最常见的 Extension 正是日志与追踪,它们都做 I/O。

### 可选方向

1. `EventObserver` 默认 advisory,其它五种保持 blocking;观察者本来就不该有否决权
2. 给每次 Extension 调用套一个可配置的 deadline,超时按 extension_failure 处理
3. 保持现状,文档加粗告诫,并提供一个「异步观察者」的官方包装器

建议 1 加 3。

### 退出条件

```text
一个阻塞 100ms 的 EventObserver 不会让 Run 慢 100ms 乘事件数,或文档明确说明会
```

## A09 规范强度高于实现成熟度

- [ ] 决策:引入 conformance 矩阵

### 现状

docs 与 specs 约四千七百行,非测试 Go 代码约九千行。specs 里 MUST 密集,实现是 PoC。T13 已经列了一批漂移,而且这些漂移在 M13 宣称「wire contract 冻结」之后仍然存在。specs 先行是这个项目的方法论,不是问题;问题是没有机制让 MUST 与测试对账。

### 可选方向

在 `specs/` 加一份 conformance 矩阵:每条 MUST 一行,列出对应测试函数或标 FUTURE。CI 里加一个脚本,矩阵引用的测试函数必须存在。specs 的 Status 字段从 Draft 改为 Draft / Implemented / Partial 三档。

### 退出条件

```text
每条 MUST 都能追溯到测试或 FUTURE 标记
新增 MUST 没有测试时 CI 失败
```

## A10 Orchestrator 单锁单进程

- [ ] 决策:是否在 D02 之前动它
- [ ] 依赖 D02

### 现状

一把 `mu` 保护路由表、容量计数与全局 FIFO 队列。`CancelRun` 与 `CloseAgent` 是全表扫描,`orchestration/orchestrator.go`。路由表在内存,store 是文件,多进程被 specs/12 列为 Reserved。PoC 够用。

### 建议

D02 若选 Embedded,这条不动,只把 `CancelRun` 改成通过 RunID 到 Conversation 的索引直查。D02 若选 Hosted,这一层需要重写:路由表外置、租约用 store 的 CAS 实现、队列按 Conversation 分片。那是另一个里程碑,不在本 TODO 范围内。

### 退出条件

```text
CancelRun 与 CloseAgent 不再全表扫描
```

---

# 定位决策

这一部分回答「立得住吗」。结论:作为设计论点立得住,作为产品还没有。真正差异化的只有一件事,就是把 Agent 当成有生命周期的 Go 执行单元:单飞、Idle 与 Busy、在定义好的安全边界上消费 Steer 与 FollowUp、退休与复活加 generation 围栏。这是 actor 模型的取法,Go 生态里的 chain / graph 框架没有人这样做,这个位置是空的。

它输在广度:两种 OpenAI 形态的 API,没有 MCP,没有压缩、记忆、评测、OTel,Hosted 是单 Pod,没有 license 与社区。

## 竞品参照

| 类别 | 代表 | 形态 | Gotato 相对位置 |
| --- | --- | --- | --- |
| Go 框架 | Google ADK Go、字节 Eino、LangChainGo、Genkit Go | chain / graph,请求作用域,电池齐全 | 语义更严,广度差一个量级 |
| durable execution | Temporal、Restate | 用工作流引擎兜住 agent 持久化 | 它们解决 A04 的问题,且已成熟 |
| 托管 runtime | OpenAI / Anthropic Agent SDK、Bedrock AgentCore、Vertex Agent Engine、LangGraph Platform | 云厂商的地盘 | Hosted 路径在这里赢不了 |
| 灵感来源 | Pi | TypeScript,有 compaction 与 session tree | Gotato 是它的 Go 侧写,但缺 compaction |

目标用户是已经有基础设施、原本要自己手写这套东西的 Go 平台团队。这个群体小,但会为正确的生命周期语义买单。项目一旦往「半个框架」方向长,这点价值就会被 ADK 与 Eino 吞掉。

## D01 产品论点的兑现顺序

- [ ] 待拍板

「Conversation 可以跨 Agent 生命、跨重启存活」是 README 与 docs 的核心承诺。A03 与 A04 说明它目前只成立一半。建议顺序:

```text
1. A03 压缩            没有它长会话必死,论点不成立
2. A04 提交日志或按 Run 快照   没有它 durable 是虚词
3. T01 / T03           修复已宣称完成的 M12 / M13
4. 再谈广度(D04)
```

## D02 Embedded 还是 Hosted

- [ ] 待拍板

建议收紧到 Embedded。Host 作为参考部署保留,单 Pod,多进程路由明确写成不做,直到出现一个真实用户要求它。理由:Hosted 是云厂商的主场;Embedded 是「嵌进已有 Go 服务,一个 Model 加一个 Tool,生命周期正确」,这是小团队能赢的位置,也是 A07 与 A10 该不该动的前提。

如果选 Hosted,A07 与 A10 升为 P0,并需要一个新的里程碑处理路由外置与多 Pod 连续性。

## D03 与 Pi 的关系

- [ ] 待拍板

现在是悬着的:gateway 读 Pi 的 auth.json、冒 Pi 的 originator,但 Core 没有 Pi 的 compaction 与 session 格式。二选一:

```text
做 Pi 的 Go 同类   对齐 session 格式与 compaction,保留凭据复用并公开声明风险(T11)
独立项目           去掉对 Pi auth.json 的耦合,Codex 适配器改为标准 API key 或自有 OAuth
```

两条路都比现状好。现状是承担了耦合的风险,却没拿到兼容的收益。

## D04 广度补哪些

- [ ] 待拍板

在 D01 三步之后,建议按这个顺序补,每一项都有现成的落点:

```text
MCP ToolSet          ToolSet 接口就是为它设计的,specs/13 已列
Anthropic 适配器     第二个非 OpenAI 形态的 provider,检验 Model 契约是否真的 provider-neutral
testkit 导出         A02 的一部分,specs/11 的要求
一个真实集成示例     把 Agent 嵌进一个已有的 HTTP 服务,不是 cmd 里的 demo
```

不建议补的:graph / workflow 编排、记忆与 RAG、评测框架。它们把项目推向「半个框架」,正是 D02 要避免的方向。
