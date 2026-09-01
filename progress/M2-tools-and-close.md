# M2 — Tool 契约与 Close 语义

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 2（Tool 侧） · Slice 3（deadline 与 abort） | M1 |

## 目标

让 Model → Tool → Model 这条链在 Core 里跑通，并把 Agent 的关闭语义钉死：关闭是幂等的、Context 有界的，而且不会把忽略 Context 的工作伪报成已关闭。

## 交付项

- [x] 基础 Tool 契约与参数完整组装（`agent.go:624` 内的 Tool 分支）
- [x] Tool 参数 Schema 校验：`validateToolSchema` / `validateSchemaValue` / `schemaTypeMatches`（`agent.go`）
- [x] 恶意或残缺 JSON 不进 executor
- [x] Tool executor 单个 ToolUse 至多执行一次，panic 被捕获（`agent.go` 的 `executeToolSafely`）
- [x] Tool Result 提交与大小上界（`agent.go` 的 `validateToolResultSize`）
- [x] Run / Model / Tool 三级 deadline（`limits.go:14`）
- [x] Abort 路径：`RunCanceler.CancelRun`（`agent.go`，用例 `agent_test.go:168`）
- [x] `Closing` / `Closed` 状态与幂等 Close
- [x] Busy 时 Close 有界等待，超时后保留可观测的 `Closing`（`agent_test.go:245`）
- [x] Core snapshot 往返：`Snapshotter` 与 `WithInitialSnapshot`（用例 `agent_test.go:314`）
- [x] typed 函数 Tool helper：`WithFunc` / `NewFuncTool` / `NewFuncToolWithProgress`（`toolfunc.go`）
- [x] 反射生成 InputSchema，支持 string、bool、整数、浮点、slice、map、嵌套 struct、匿名内嵌（`toolfunc.go` 的 `valueSchema`）
- [x] 字段规则跟随 `encoding/json`：`json` tag 改名、`-` 跳过、`omitempty` 与指针表示可选
- [x] `description` tag 写字段说明，`enum` tag 写允许的字符串值
- [x] 不可描述的类型在 `NewAgent` 阶段失败，而不是等到第一次 Model 请求

## typed 函数 Tool

注册一个普通 Go 函数只要一行：

```go
agent, err := gotato.NewAgent(
    gotato.WithModel(model),
    gotato.WithFunc("get_weather", "Return the weather.", getWeather),
)
```

输出类型的处理规则：`string` 变成文本内容，`ToolResult` 原样返回，其余类型序列化成 `ContentJSON`。函数返回的 error 走 Tool Result 语义，不终止 Run。

helper 的边界见 `specs/05-tools-and-toolsets.md` 的组合一节：依赖与顺序保持显式，不做包级全局发现。

顺带修掉一个潜在冲突：`NewAgent` 原来把 `ToolSpec.ID` 和 `ToolSpec.Name` 无条件都注册进同一张表，两者相等时会误报重名。现在只在 Name 与 ID 不同时才额外注册。

## 退出条件

```text
scripted Tool Call 可以完成 Model → Tool → Model
malformed arguments 不执行 Tool
Close 后不产生新 Run
并发 Close 只释放一次资源
不响应 Context 的 Model/Tool 不会被伪造为已关闭
一个普通 Go 函数可以用一行注册成 Tool
```

## 测试场景

| 场景 | 内容 | 用例 |
| --- | --- | --- |
| C | Prompt 阻塞 → 短 Context Close → 调用方拿到超时错误 → Agent 仍可观测为 `Closing` → 放开 Model → `Done` 关闭且状态变 `Closed` | `agent_test.go:245` |

typed helper 的用例在 `toolfunc_test.go`：Schema 推导、非法输入类型被拒、跑通整条 Loop、缺必填字段时 executor 不执行、函数错误变成失败的 Tool Result、字符串输出变文本。

## 验收命令

```bash
go test -run 'TestAgentToolLoop|TestAgentCloseWhileBusy|TestAgentCancelRun|TestFuncTool|TestWithFunc' -race ./...
```
