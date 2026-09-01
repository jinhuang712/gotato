# M7 — Core 控制命令与 Continue

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 4（Core composition 的命令入口部分） | M2、M6 |

## 目标

在同一个 canonical Loop 上加出 `Continue`、`Steer`、`FollowUp` 三个附加能力，让高级调用方不必换一套执行 API，也不把 Core 变成面向用户的调度器。

## 交付项

- [x] `ControllableAgent` additive interface，最小 `Agent` 接口保持两个方法（`agent.go`）
- [x] `Continue`：不追加 user Message，仅在 transcript 停在 user 或 Tool Result 时合法（`validateContinuable`）
- [x] `Steer`：在下一个 Turn 边界被消费，不打断在飞的 Model 或 Tool 调用
- [x] `FollowUp`：只在 Run 本该结算的边界被消费，容量有界
- [x] `Abort`：取消当前 Run，无 Run 时是空操作，不关闭 Agent
- [x] 控制命令走各自的有界 channel，容量由 `CoreLimits` 的 `MaxSteerMessages` 与 `MaxFollowUpMessages` 决定（`limits.go`）
- [x] 缓冲满返回 `ErrLimitExceeded`，容量为 0 表示该控制命令被禁用
- [x] `Closing` 与 `Closed` 之后一律拒绝控制命令
- [x] 两个安全边界都先查 Context 再消费控制消息
- [x] Run 走到终态时丢弃残留控制消息，不让它们泄进下一个 Run

## 两个安全边界

```text
Turn 边界（turn_end 之后，选择是否继续之前）
  → 检查 Run Context
  → 消费 Steer
  → 进入下一个 Turn

结算边界（Model 不再要求 Tool，Run 本该结束）
  → 检查 Run Context
  → 消费 Steer
  → 消费 FollowUp
  → 有注入就再走一个 Turn，没有就发 agent_end
```

两条命令的差别是消费时机，不是语义强弱：

| 命令 | 消费时机 | 到达 Model 的时间 |
| --- | --- | --- |
| Steer | 每个 Turn 边界 | Tool 循环还在跑时，下一次 Model 请求就带上 |
| FollowUp | 只在结算边界 | Model 停止要求 Tool 之后才带上 |

Steer 落在结算边界时会让 Run 多跑一个 Turn，而不是被丢掉：Protected 语义不允许静默丢弃已接受的控制消息。

注入的控制消息作为 user Message 提交进 transcript，复用 `message_start` 与 `message_end` 两个 canonical 事件，payload 带 `source` 字段区分 `steer` 与 `follow_up`。canonical Event 种类没有增加。

## Abort 与 CancelRun 并存

两个都保留，分工不同：

- `Abort()` 取消当前 Run，调用方不需要知道 RunID
- `CancelRun(ctx, runID)` 指定 Run 取消，用于 Host 侧按 ID 路由，带 fencing

## 退出条件

```text
Continue 不追加 user Message                              ✅
transcript 停在不可继续状态时 Continue 返回类型化错误      ✅
Steer 在安全边界被消费，不打断当前 Model 或 Tool           ✅
FollowUp 容量有界，溢出行为显式                            ✅
控制命令不制造第二个 Loop 实现                             ✅
Closing 之后所有控制命令被拒绝                             ✅
一个 Run 仍然只有一个终态 agent_end                        ✅
```

## 测试

用例在 `control_test.go`，`recordingModel` 记录每次 Model 调用收到的 transcript，用来断言控制消息到底在第几个 Turn 到达：

| 用例 | 断言 |
| --- | --- |
| `TestContinueAppendsNoUserMessage` | Continue 不合成用户输入，transcript 停在 assistant 时被拒 |
| `TestSteerIsConsumedAtTheNextTurnBoundary` | Steer 不进在飞的那次 Model 调用，进下一次 |
| `TestSteerAtSettlementKeepsTheRunGoing` | 结算边界的 Steer 让 Run 多跑一个 Turn，`agent_end` 仍然只有一个 |
| `TestFollowUpWaitsForSettlement` | FollowUp 不在 Turn 边界被消费，结算边界才进 |
| `TestControlBuffersAreBounded` | 缓冲满与容量为 0 各自的类型化错误 |
| `TestControlCommandsRejectedAfterClose` | Close 之后三个命令都被拒 |
| `TestAbortCancelsTheCurrentRun` | Abort 取消 Run，Agent 回到 Idle |
| `TestControlMessagesDoNotSurviveAFailedRun` | 失败 Run 的残留控制消息不进下一个 Run |

## 验收命令

```bash
go test -race -run 'TestContinue|TestSteer|TestFollowUp|TestControl|TestAbort' .
```

## 遗留

控制命令目前只在 Core 与进程内调用方可用。把 Steer 与 FollowUp 投影到 wire 协议是 M13 的事，`specs/09-agent-service-and-grpc.md` 的 `RunCommand` 草案已经给出了对应的命令形状。
