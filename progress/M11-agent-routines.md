# M11 — Agent Routines 与 spawn

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 未开始 | Slice 7（Agent Routine coordination） | M10 |

## 目标

让一个 Agent 能创建另一个独立的 Agent Routine，并把这层关系限定成纯相关性：有 ID 可以对上，但没有资源所有权，没有自动的取消继承。

## 现状

| 事项 | 现状 |
| --- | --- |
| spawn 路径 | 不存在 |
| `SpawnID` / `OriginRunID` | `Event` 结构里有字段（`events.go:43`），无任何生产者 |
| routine 事件 | `routine_started` 等四个 kind 未定义 |
| group 协调 | 不存在 |

## 交付项

- [ ] spawn 入口：Agent goroutine 直接创建，或经 factory 与 Orchestration channel 创建
- [ ] 被创建的 Agent 有独立 AgentID、私有状态、独立 channel、独立限制与独立生命周期
- [ ] spawn 溯源字段：`SpawnID`、origin AgentID、origin RunID，仅作相关性
- [ ] Agent 之间用 channel 承载命令、结果与事件，不直接改对方状态
- [ ] `routine_started`、`routine_completed`、`routine_failed`、`routine_cancelled` 四个事件，终态归 Protected
- [ ] group 协调器：collect-all、fail-fast、collect-partial、first-success 四种策略
- [ ] 子 Agent 的退休归属：ephemeral child、persistent child、workflow-owned child 三种，由 Orchestration 显式指定

## 边界

```text
父 Agent 完成不自动关闭子 Agent
一个 Agent 的失败或取消不自动终止另一个
spawn 溯源不等于生命周期所有权
级联取消是显式的 Orchestration 策略，不是默认行为
每个 Routine 和 Run 有自己的事件序列，溯源不合并事件历史
```

## 需要先定的事

| 决策 | 说明 |
| --- | --- |
| Agent handle 形态与 channel 关闭协议 | open question，决定 spawn 返回什么 |
| 子 Agent 由谁创建 | Core、应用还是 Host factory |
| 哪些 spawn 元数据是规范性的 | SpawnID、origin AgentID、origin RunID 三选几 |
| Agent 之间的 channel 语义 | 请求响应、事件，还是两者都要 |
| routine 组归 Core 还是 Orchestration | 影响 group 协调器放哪个包 |
| 独立子 Agent 的事件是否共用一条流 | 影响 Host 投影 |

## 退出条件

```text
spawn 出的 Agent 有独立身份与独立事件序列
父 Run 结束不会关闭子 Agent
子 Agent 失败不会隐式终止父 Agent
四种 group 策略各有断言用例
级联取消只在显式选择时发生
routine 终态事件不被静默丢弃
```

## 验收命令

```bash
go test -race ./...
```
