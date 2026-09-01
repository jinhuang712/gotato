# M9 — ToolSets 与有界并行 Tool

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 4（Core composition 的 Tool 调度部分） | M8 |

## 目标

把 Tool 从一个扁平列表升级成可分组、可分阶段暴露的能力集合，并让一个 Turn 里的多个 Tool 能在明确上界内并行执行。

## 交付项

### ToolSet

- [x] `ToolSet` 接口：`Spec()` 加 `Tools(context.Context)`（`toolset.go`）
- [x] `WithToolSet` 装未激活的，`WithActiveToolSet` 装开箱可见的
- [x] 限定身份 `ToolSetName + "." + ToolName`，根命名空间由 `WithRootNamespace` 配置，默认为空
- [x] 可见性排序确定：按限定 ID 排序
- [x] 激活工具 `activate_toolset`，一个必填 `name` 字段，未激活集合按字典序进 enum
- [x] 激活走普通 Tool 路径，Pre 与 Post 链照常生效
- [x] 解析发生在 Execute 里，可见性提交在批次边界，新工具只在下一次 Model 请求出现
- [x] 激活幂等：重复激活不重新解析、不产生重复工具
- [x] 解析失败不暴露任何工具，失败作为 Tool Result 回给 Model
- [x] `toolset_activated` 事件，归类 Protected
- [x] 激活状态进 `CoreSnapshot.ActiveToolSets`，rehydrate 之后工具仍然可见
- [x] 构造期校验：nil ToolSet、重复 ToolSet 名、名字带点、集合内重复本地名、重复限定 ID、非法 Schema
- [x] `MaxActiveToolSets` 上界

### 并行执行

- [x] `CoreLimits.MaxParallelTools` 上界，默认 1（顺序执行）
- [x] preflight 始终按源顺序：解析、JSON 校验、Schema 校验、Pre 链（`toolbatch.go`）
- [x] `ToolSpec.Sequential` 生效：该工具独占一组，不与任何工具并行
- [x] 完成事件在结果到达时就地发出，按实际完成顺序；Tool Result Message 按 assistant 源顺序提交
- [x] 每个被准入的 Tool 在 `turn_end` 之前都有唯一终态
- [x] worker 的进度回调经由单条 channel 回到 Agent goroutine 排序发事件

### JSON Schema 子集

- [x] 子集边界写进 `validateToolSchema` 的文档注释
- [x] `enum` 强制校验，越界值不进 executor

## 三个决策

| 问题 | 决定 | 理由 |
| --- | --- | --- |
| 一个 Sequential Tool 是否强制整批串行 | 不强制。它独占一组，同批其余工具照常并行 | 保住它要的独占性，不牺牲整批吞吐。副作用是它会把批次切开：夹在两个并行工具中间时，前后两段各自成组 |
| 根命名空间默认值 | 空。根 Tool 保持自己声明的 ID | 已有调用方零改动；要命名空间的显式开 `WithRootNamespace` |
| 并行默认开不开 | 默认 `MaxParallelTools = 1` | 并行会改变完成事件的顺序，属于可观察行为变化，交给调用方显式开启 |

## Schema 子集边界

Core 校验的是 JSON 值而不是 Go struct，所以 Go 侧的 `omitempty` 不会改变 `required` 的含义。

强制执行：

```text
type                  object · array · string · boolean · number · integer · null
properties            递归校验已声明成员
required              已声明成员必须存在
additionalProperties  false 时拒绝未声明成员
enum                  值必须落在声明的取值里
```

透传给 Model 但 Core 不校验：`description`、`format`、`items` 以及其余关键字。未知关键字被忽略而不是拒绝，这样更丰富的 provider Schema 可以原样穿过。

enum 强制校验带来一个连带后果：激活工具的 enum 只列未激活的 ToolSet，所以跨批次重复激活同一个集合会被参数校验挡下。真正需要幂等的是同一批次里的重复调用，那条仍然成立。

## 批次执行顺序

```text
preflight（源顺序）
  → 解析 Tool
  → JSON 合法性
  → Schema 校验
  → Pre 链
  → 得到 plan，含被 block 的项
分组
  → Sequential 工具独占一组
  → 其余按 worker 上界切组
执行
  → 组内并发，进度与完成信号回到 Agent goroutine
  → tool_execution_end 按实际完成顺序发出
提交（源顺序）
  → Post 链
  → 结果大小校验
  → 提交 Tool Result Message
  → tool_result_committed 按源顺序发出
批次边界
  → 提交 ToolSet 激活
  → toolset_activated
turn_end
```

## 退出条件

```text
激活一个 ToolSet 之后，新工具只在下一次 Model 请求出现   ✅
激活失败不会部分暴露工具                                ✅
重复激活是幂等的                                        ✅
并行批次的完成事件按完成顺序，提交按源顺序              ✅
并行 worker 数不超过配置上界                            ✅
构造期能拦住重名与命名空间冲突                          ✅
Schema 子集边界有文档，越界输入行为确定                  ✅
```

## 测试

用例在 `toolset_test.go`：

| 用例 | 断言 |
| --- | --- |
| `TestSnapshotCarriesActiveToolSets` | 激活状态进快照，rehydrate 之后限定名工具仍可见、激活工具不再出现 |
| `TestSnapshotWithUnknownToolSetFailsLoudly` | 快照点名一个没安装的 ToolSet 时构造直接失败，不静默丢能力 |
| `TestToolSetStaysHiddenUntilTheNextRequest` | 第一次请求只看到激活工具，第二次才看到限定名工具，激活事件恰好一个 |
| `TestToolSetActivationIsIdempotent` | 重复激活不重新解析、不产生重复工具 |
| `TestFailedToolSetResolutionExposesNothing` | 解析失败变成失败 Tool Result，工具仍然不可见，激活工具还在 |
| `TestToolSetConstructionValidation` | 重名、带点、集合内重名、nil、非法根命名空间，以及根命名空间生效 |
| `TestParallelToolsCommitInSourceOrder` | 两个 Tool 同时在执行器内（barrier 证明），完成事件按完成顺序、提交与 transcript 按源顺序 |
| `TestSequentialToolRunsAlone` | Sequential 工具在并行批次里并发峰值仍为 1 |
| `TestParallelWorkerBoundIsRespected` | 4 个调用、上界 2，第三个必须等第一组结算，峰值恰好 2 |

## 验收命令

```bash
go test -race -run 'TestToolSet|TestFailedToolSet|TestParallel|TestSequentialTool|TestSnapshot' .
```
