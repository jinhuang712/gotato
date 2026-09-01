# M11 — Agent Routines 与 spawn

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 7（Agent Routine coordination） | M10 |

## 目标

让一个 Agent 能创建另一个独立的 Agent Routine，并把这层关系限定成纯相关性：有 ID 可以对上，但没有资源所有权，没有自动的取消继承。

## 设计自省：factory 归谁

`specs/15` 把「spawned Agent 由 Core、应用还是 Host factory 创建」列为未决问题。照 docs 判：

- `docs/05` 第 3 节列 Core 的 moving parts，到「一个 Agent 自己的执行」为止，没有 factory
- `docs/05` 第 4 节列 Orchestration 的 moving parts，第一项就是 `Agent Factory`
- `docs/07` 第 8 节明说 `AgentFactory` 是 Orchestration 组件，不是 Core Extension

结论：**spawn 归 Orchestration**。Core 永远不自己构造 Agent；一个 Core Agent 想派生，走应用装的能力（Tool 或 Extension）调到这条路径。

## 交付项

- [x] `Spawn` 创建独立 Agent Routine，自带独立 Conversation（`orchestration/spawn.go`）
- [x] 被创建的 Agent 有独立 AgentID、私有状态、独立限制与独立生命周期
- [x] 溯源字段 `Provenance`：`SpawnID`、origin AgentID、origin RunID，仅作相关性
- [x] 溯源记在子 Conversation 的 `Record.Origin` 上，关掉源头不影响子 Agent
- [x] group 协调器：collect-all、collect-partial、fail-fast、first-success 四种策略
- [x] 四种策略都不取消兄弟；取消是显式 opt-in
- [ ] `routine_started` 等四个事件——见下方推迟说明

## routine 事件为什么推迟

`specs/04` 把四个 routine 事件列进 Core 的事件种类，但 `docs/04` 第 10 节说得更具体：**被派生的 Agent 有自己的事件通道与序列**，Orchestration 或 Host 可以用显式相关性把选中的事件投影到源头的流上，而且「投影不得假装两个独立 Agent 共用一条 transcript 或一条事件序列」。

也就是说这四个事件是**投影产物**，不是某个 Run 的 per-Run 序列成员。把它们塞进 Core 的 sequence 会直接违反「每个 Routine 与 Run 有自己的序列」。投影的设计属于 M13 的 Host 侧，放到那里一起做。

## 四种 group 策略的分工

| 策略 | 等到什么时候 | 什么时候算 group 失败 |
| --- | --- | --- |
| collect-all | 全部成员结算 | 任一成员失败 |
| collect-partial | 全部成员结算 | 永不失败，调用方自己读每条结果 |
| fail-fast | 第一个失败 | 报那个失败 |
| first-success | 第一个成功 | 全部失败时 |

停止等待**不等于**取消。`docs/08` 第 8 节说得明白：这些是协调策略，不建立资源所有权也不建立取消继承。group 返回时还在跑的成员会继续跑完，它那条结果标为未结算。要取消兄弟得显式打开 `CancelSiblings`。

## 边界

```text
父 Agent 完成不自动关闭子 Agent
一个 Agent 的失败或取消不自动终止另一个
spawn 溯源不等于生命周期所有权
级联取消是显式的 Orchestration 策略，不是默认行为
每个 Routine 和 Run 有自己的事件序列，溯源不合并事件历史
```

## 已定的事

| 决策 | 结论 |
| --- | --- |
| 子 Agent 由谁创建 | Orchestration。`docs/05` 把 Agent Factory 列在 Orchestration 的 moving parts 里 |
| 哪些 spawn 元数据是规范性的 | 三个都要：`SpawnID`、origin AgentID、origin RunID，合成 `Provenance` |
| routine 组归哪个包 | Orchestration。`docs/08` 第 8 节把四种策略定为 Orchestration 或 Host 策略 |
| 独立子 Agent 的事件是否共用一条流 | 不共用。投影是 Host 的事，归 M13 |

`Spawn` 返回的是普通 `gotato.Agent` 句柄，没有引入新的 handle 形态——channel 关闭协议这个未决问题因此不再阻塞任何东西。

## 退出条件

```text
spawn 出的 Agent 有独立身份与独立事件序列        ✅
父 Run 结束不会关闭子 Agent                      ✅
子 Agent 失败不会隐式终止父 Agent                ✅
四种 group 策略各有断言用例                      ✅
级联取消只在显式选择时发生                       ✅
routine 终态事件不被静默丢弃                     推迟到 M13
```

## 测试

| 用例 | 断言 |
| --- | --- |
| `TestSpawnCreatesAnIndependentRoutine` | 子 Agent 自带 Conversation 与身份，关掉源头之后仍可用 |
| `TestGroupCollectAllWaitsForEveryMember` | 全部成员结算才返回 |
| `TestGroupCollectAllReportsAMemberFailure` | 等齐之后照实报失败 |
| `TestGroupCollectPartialNeverFailsTheGroup` | 成员失败不拖垮 group |
| `TestGroupFailFastStopsWaitingWithoutCancellingSiblings` | 停止等待但兄弟继续跑完 |
| `TestGroupCancelSiblingsIsOptIn` | 显式打开才取消兄弟 |
| `TestGroupFirstSuccessStopsAtTheFirstWin` | 第一个成功即返回 |
| `TestGroupFirstSuccessFailsWhenEveryMemberFails` | 全挂时 group 失败 |
| `TestGroupHonoursTheCallerContext` | 调用方 Context 到期时 group 退出 |

## 验收命令

```bash
go test -race ./...
```
