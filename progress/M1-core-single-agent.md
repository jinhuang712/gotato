# M1 — Core 单 Agent 与 Run Events

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 1 · Slice 3（Core 事件部分） | M0 |

## 目标

一个普通 Go 程序用脚本化 Model 构造 Agent，调一次 Prompt 拿到结果，全程不需要 Runner、Host、SessionService 或任何平台依赖。

## 交付项

- [x] `Agent` 最小接口 `Prompt` / `Close`（`agent.go`）
- [x] Agent 私有状态与单一执行权限：goroutine + command channel（`agent.go:458` 的 `loop`）
- [x] canonical Model → Tool → Model Loop（`agent.go:624` 的 `executeRun`）
- [x] `Prompt`、`RunResult`、基础 `Message`（`types.go`）
- [x] Deterministic Model：`EchoModel` / `DemoModel`（`internal/testmodel/model.go`）
- [x] single-flight 与 busy 错误分类（`agent.go` 的 `admissionError`）
- [x] Context cancellation（`agent.go` 的 `boundedContext` / `contextFailure`）
- [x] canonical Run Events，`Sequence` 每个 Run 从 1 起独立递增（`events.go:34`）
- [x] Event 分类 Protected 与 Coalescable（`events.go:13`）
- [x] 有界本地订阅：订阅缓冲 128，Coalescable 溢出丢弃、Protected 溢出直接失败该消费者（`events.go:159`）
- [x] Agent 生命周期 `Created → Idle ⇄ Busy → Closing → Closed`（`agent.go` 的 `AgentStatus`）
- [x] lifecycle 信号独立于 Run sequence（`events.go:49` 的 `LifecycleKind`）

## 退出条件

```text
Prompt 能得到确定结果
同时提交两个 Prompt 不会并发修改 Agent state
一个 Run 只产生一个终态 agent_end
Run 结束后 Agent 仍可再次 Prompt
Protected Event 不被静默丢弃
```

## 测试场景

| 场景 | 内容 | 用例 |
| --- | --- | --- |
| A | 构造 Agent → Prompt → 断言终态结果 → 断言 `agent_end` 恰好一次 → 再 Prompt → Close | `agent_test.go:66` |
| B | Prompt A 阻塞在 Model 中，提交 Prompt B 得到 busy，放开 A 后 A 结算恰好一次 | `agent_test.go:220` |

## 验收命令

```bash
go test -run 'TestAgentPrompt|TestAgentSingleFlight' -race ./...
```

## 遗留

无。
