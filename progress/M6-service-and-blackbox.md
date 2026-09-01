# M6 — 服务组装、drain 与黑盒验收

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 6（Hosted Agent Service 的一阶段范围） | M3、M5 |

## 目标

把 Library 组装成一条命令能起的本地服务，并用一条真实进程的黑盒测试证明这条链是通的。`cmd/gotato-agent` 只做依赖注入、配置和进程信号处理，不含 Agent Loop、Conversation 路由或退休实现。

## 交付项

- [x] `cmd/gotato-agent` 组装 orchestrator、host、model 与 tool（`cmd/gotato-agent/main.go:40`）
- [x] 默认 `-model echo`，首次运行零配置、不碰网络（`cmd/gotato-agent/main.go:42`）
- [x] gateway 模式按 YAML 配置接入真实 provider
- [x] Run、Model、Tool 三个 deadline 由命令行 flag 注入
- [x] SIGINT 与 SIGTERM 触发 drain 再 shutdown（`cmd/gotato-agent/main.go:93`）
- [x] readiness 在 drain 后转 false（`host/server.go` 的 `ready` 标志）
- [x] drain 期间停止新 admission 与新 Agent creation（`orchestration/orchestrator.go:575`）
- [x] drain 行为的断言测试（`host/lifecycle_test.go:146`）
- [x] 真实本地进程的 black-box 测试（`cmd/gotato-agent/main_test.go:22`）

## drain 与黑盒

### drain 断言测试

一阶段测试场景 H 覆盖这条链：

```text
启动服务 → 启动一个 active Run → 触发 drain
→ 断言 readiness 为 false
→ 断言新请求被拒绝
→ 让 active Run 结算或取消
→ 关闭或退休 Agent
→ 断言有界完成，或给出 incomplete-drain 报告
```

关键判据是最后一条：drain deadline 到期时不得把仍在 `Busy` 或 `Closing` 的 Agent 标记为已关闭。

`Orchestrator.Drain` 现在返回 `*DrainIncomplete`，里面按 ConversationID 列出没结算的条目与它们各自的 Agent 状态；`POST /admin/drain` 把这份清单以 `incomplete_drain` 加 `pending` 数组的形式返回 503。重复调用 Drain 会重新汇报当前状态，而不是第二次直接返回成功。

### black-box 进程测试

`TestBlackBoxLocalAgent` 编译出真实二进制、起进程、发 HTTP 请求，覆盖一阶段成功标准的十步：

```text
1. 创建或解析一个 Conversation
2. 提交 Prompt 并得到 RunResult
3. 通过 SSE 观察 canonical Events
4. 确认 Run 结束后 Agent 仍是 Idle
5. 用相同 ConversationKey 再次访问同一个 live Agent
6. 显式退休 Agent，Conversation 进入 Dormant
7. 再次访问该 Conversation，得到新 AgentID 与递增的 generation
8. 确认旧 handle 与旧 generation 不再接收新请求
9. 显式关闭 Agent，确认新的 Prompt 被拒绝
10. 触发 drain，确认新请求停止、现有请求按策略结束
```

整条测试不得依赖真实 LLM、API Key、数据库、Redis、gRPC、Kubernetes、Service Registry 或 Message Broker。

## 退出条件

```text
drain 后 readiness 为 false
drain 后不创建新 Agent、不 dispatch 新 Run
deadline 到期不会虚报所有 Agent 已关闭
完整测试流程不依赖外部服务
go test -race ./... 全绿
```

## 验收命令

```bash
gofmt -l .
go vet ./...
go test -race ./...
```
