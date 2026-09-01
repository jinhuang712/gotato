# M8 — Extensions 六个挂点

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 4（Core composition 的阶段挂点部分） | M7 |

## 目标

在 Loop 的具名阶段上开出扩展点，让调用方能改上下文、改转换、拦 Tool、观察事件、决定停不停，同时不把 Agent 私有状态交出去。

## 交付项

- [x] `ContextTransformer`：收只读 `ContextSnapshot`，返回新的 Message 列表（`extensions.go`）
- [x] `MessageConverter`：按安装顺序运行，输出只进 ModelRequest，不进 transcript
- [x] `PreToolUse`：在完整组装、解析、Schema 校验之后运行，首个 block 或阻塞式失败结束整条链
- [x] `PostToolUse`：按安装顺序的逆序运行，收 executed、blocked、failed、cancelled 四种结果
- [x] `EventObserver`：按生产顺序收 canonical Event，Core 在 `emit` 里等待它返回
- [x] `TurnStopper`：在 `turn_end` 之后、选择是否继续之前运行
- [x] `WithExtension` / `WithExtensions` 安装，顺序在 Agent 生命周期内不可变
- [x] 一个值实现多个扩展接口时装进每一条链；一个都不实现时构造期报错
- [x] 失败语义：默认阻塞，实现 `AdvisoryExtension` 才降级为建议式
- [x] panic 被 `guard` 捕获，转成 `extension_failure`，Agent goroutine 不受影响
- [x] 重入被拒：被等待的扩展阶段里调用同一个 Agent 的 Prompt 拿到 `busy`，不会死锁

## Loop 里的挂点位置

```text
turn_start
  → ContextTransformer 链（正序）
  → MessageConverter 链（正序）
  → Model 流
message_end
  → 每个 Tool：组装 → 解析 → Schema 校验
      → PreToolUse 链（正序，首个 block 截断）
      → executor（block 时跳过）
      → PostToolUse 链（逆序，四种结果都过）
      → 提交 Tool Result
turn_end
  → TurnStopper 链（首个 stop 截断）
  → 控制消息安全边界
  → 继续或结算
```

`emit` 现在返回错误：观察者在事件发布前被等待，阻塞式失败直接结算当前 Run。终态 `agent_end` 无论如何都会发出去，一个 Run 仍然恰好一个终态事件。

## Core 守住的不变量

- Post 链改不了 `CallID` 与 `Executed`：Core 在每一跳之后重新盖回去
- 被 block 的 Tool 拿 `blocked` 状态、`Executed == false`，executor 一次都不调
- Transformer 与 Converter 的输出只进 ModelRequest，不进 committed transcript
- 扩展只能靠显式 Context 加结果 channel 调度应用侧工作，禁止无界或发射后不管的 goroutine

## 边界

这几样仍然是 Orchestration 或 Host 组件，不做成 Core Extension：

```text
AgentFactory · ConversationResolver · AdmissionController
AgentCache · RetirementPolicy · EventProjector · EventBridge
ErrorMapper · DrainPolicy
```

## 退出条件

```text
六个挂点都有安装入口与顺序断言                                ✅
blocked 的 Tool 结果 Executed 为 false，且 executor 没被调用   ✅
Post 链收得到 blocked 与 failed 结果，且不能篡改 Executed       ✅
观察者 panic 被恢复，Loop 不受影响                             ✅
阻塞式扩展失败会结算当前 Run，建议式不会                       ✅
```

## 测试

用例在 `extensions_test.go`：

| 用例 | 断言 |
| --- | --- |
| `TestToolStagesRunInOrderAndReverseOrder` | Pre 正序、Post 逆序，身份与 Executed 被 Core 恢复 |
| `TestPreToolUseBlockSkipsTheExecutor` | block 之后 Pre 链截断，executor 零调用，结果是 blocked |
| `TestTransformersShapeTheModelViewOnly` | 注入内容进了 Model 视图，没进 transcript |
| `TestObserverSeesProductionOrder` | 观察者按生产顺序收到 `agent_start` 到 `agent_end` |
| `TestBlockingObserverFailureSettlesTheRun` | 阻塞式失败让 Run 结算，Model 一次都没被调用 |
| `TestAdvisoryObserverFailureDoesNotSettleTheRun` | 建议式失败不影响结果 |
| `TestExtensionPanicIsRecovered` | panic 转成 `extension_failure`，Agent 回到 Idle |
| `TestTurnStopperPreventsTheNextModelCall` | 停止保留当前 Turn，不发起下一次 Model 调用 |
| `TestExtensionCannotReenterTheSameAgent` | 扩展里重入 Prompt 拿到 `busy` |
| `TestWithExtensionRejectsUnknownValues` | 不实现任何扩展接口的值在构造期被拒 |

## 验收命令

```bash
go test -race -run 'TestToolStages|TestPreToolUse|TestTransformers|TestObserver|TestAdvisory|TestExtension|TestTurnStopper|TestWithExtension' .
```
