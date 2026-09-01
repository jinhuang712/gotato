# M10 — 层间边界与容量

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 进行中 | Slice 5 收口 · Slice 7（前半） | M4、M9 |

## 目标

四层模型里每层只留自己职责需要的那本账，下层的内容对上层不透明。这一阶段先把泄漏堵回去，再谈策略。

```text
Host           一次交互的投递状态
Orchestration  Conversation 身份 + 一个不透明的状态块
Agent Core     唯一权威的 session transcript
```

## 已完成：三处层间泄漏

### 1. Record 不再装会话内容

`Record` 曾经带一个 `*CoreSnapshot` 字段，而且带 json tag。后果是 `GET /v1/conversations/{id}` 把整份 transcript 吐给 HTTP 客户端，`asyncRunState` 也顺着 Record 长期持有全量消息。

- [x] `Record` 收敛成纯路由视图：身份、状态、generation、StateVersion
- [x] 快照移出 `Record`，装进新的 `StoredState{Record, Snapshot}`
- [x] Host 侧因此自动不再持有会话内容，无需改 Host 代码

### 2. Orchestration 只认一个状态权威

`SnapshotStore.Save` 一直在写，但生产代码从来没调过 `Load`——rehydrate 读的是内存里的 `Record.Snapshot`。同一份状态有两个地方声称自己是真的，只有一个真被用。

- [x] `SnapshotStore` 补齐 `Load` 与 `Delete`，成为唯一权威
- [x] rehydrate 从 store 读，读不到就失败，不会静默起一个空白 Agent
- [x] 读回来的 `StateVersion` 必须与路由记录一致，否则拒绝
- [x] `Ephemeral` 退休与 `CloseConversation` 删除 store 里的状态

这条同时把 M12 的性质改了：持久化不再是「补上读那半边」，而是真的只剩「换一个 `SnapshotStore` 实现」。

### 3. 每层为自己的索引定上界

改之前全仓 `delete(` 只出现在事件订阅清理和一个递归防护里，三张索引表一张都没回收。

- [x] `Orchestrator` 的关闭会话墓碑改成有界 FIFO，`DefaultClosedRecords` 1024，`WithClosedRecordLimit` 可配
- [x] 墓碑溢出时把最旧的从 `byID` 与 `byKey` 一并摘掉
- [x] store 拒绝删除时路由照样回收：索引上界不该取决于 store 健不健康，失败仍然照实上报
- [x] Host 的轮询表改成有界 FIFO，`DefaultRunRetention` 1024，`Server.RunRetention` 可配

## 待做：策略与容量

- [ ] `AfterRun`：选定的 Run 结算之后退休该 Agent
- [ ] `AfterIdle`：Run 结算之后且无准入 Run 时开始计时，新准入取消或重置计时器
- [ ] Busy Agent 不得被淘汰，除非有显式的取消或 abort 决定
- [ ] 容量型 admission：在 Agent 构造或 dispatch 之前预留容量，恰好释放一次
- [ ] 请求队列策略：至少支持 reject-while-busy 与有界 FIFO 两种，默认值显式
- [ ] 各项上界写进配置结构：Agent 实例数、排队请求数、活跃 Run 数
- [ ] `cmd/gotato-agent` 支持注册多个 Definition

## 已定的事

| 决策 | 结论 |
| --- | --- |
| rehydrate 时校验 Agent definition 版本 | 不加。外部没有资格对 Agent 提要求，`docs/00` 第 2 节的立场是 Agent 拥有自己的状态与工作。版本对不上要不要拒绝是接入方的判断 |
| `AfterIdle` 的 TTL 默认值 | 不设框架默认值。这是配置项，由接入方按场景填 |
| 关闭会话的墓碑留多少 | 有界 FIFO，默认 1024。留着是为了让晚到的请求拿到「已关闭」而不是静默开一个新会话 |

## 退出条件

```text
Record 序列化之后不含任何 transcript 内容                ✅
store 是唯一权威，清空 store 之后会话不可恢复             ✅
丢弃式退休之后 store 里不留状态                          ✅
关闭会话的墓碑数量有上界，超出的被回收                    ✅
Host 轮询表有上界，超出的被回收                          ✅
AfterRun 与 AfterIdle 在声明的边界上真的关闭 Agent
Busy Agent 不会被 TTL 或容量淘汰
admission 容量耗尽时按配置拒绝或排队，租约恰好释放一次
```

## 测试

| 用例 | 断言 |
| --- | --- |
| `TestRecordCarriesNoConversationContent` | 序列化一个 Record 不会带出 transcript |
| `TestDiscardedConversationLeavesNoRetainedState` | 关闭会话之后 store 清空，同名 key 拿到已关闭错误 |
| `TestRehydrationNeedsTheStore` | 清空 store 之后 rehydrate 失败，不会静默起新 Agent |
| `TestClosedConversationsAreReclaimed` | 墓碑超出上界时最旧的被摘掉，最近的仍答「已关闭」 |
| `TestPollTableIsBounded` | 轮询表超出上界时最旧的 Run 不再可查 |
| `TestConversationEndpointCarriesNoTranscript` | HTTP 会话查询接口不吐 transcript |
| `TestRetirementPersistenceFailureLeavesConversationActive` | store 挂掉时退休失败照实上报，路由保持可用 |

## 顺带修掉的 flake

`TestHTTPProgressReturnsLoopFrames` 断言进度流恰好三帧，但心跳来自 `turn_end` 事件、终态来自 result 通道，两者独立投递——这正是「执行结算与投递结算分离」要的性质，所以 result 可以先到，帧数就变了。

改成两轮 Run 的确定性场景：第一轮以 Tool Call 收尾产生心跳，第二轮阻塞到测试观察到 loop 帧后才放行。断言也从「恰好三帧」改成契约本身：首帧 accepted、末帧 result、中间全是 loop、至少一次心跳。

## 验收命令

```bash
go test -race ./orchestration/... ./host/...
```
