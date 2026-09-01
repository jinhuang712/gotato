# M5 — Host 与 HTTP/SSE 适配器

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 3（远端投影） · Slice 6（第一个协议适配器） | M4 |

## 目标

用标准库 `net/http` 做一个可复用的 Host 和 HTTP/SSE 协议适配器，handler 逻辑留在 Library 里，`cmd` 不复制一份。JSON wire type 与 Core type 严格分离。

## 交付项

- [x] HTTP semantic handler 与路由表（`host/server.go:36`）
- [x] JSON adapter types 独立于 Core types（`host/server.go:82` 起的 wire struct）
- [x] 同步 Run endpoint `POST /v1/runs`
- [x] 异步 Run 提交与轮询 `POST /v1/runs/async`、`GET /v1/runs/{run_id}`（`host/async_runs.go`）
- [x] 进度投影 `POST /v1/runs/progress`，ndjson 循环帧与 heartbeat
- [x] SSE Run endpoint `POST /v1/runs/stream`（`host/server.go:145`）
- [x] Run 取消 `POST /v1/runs/{run_id}/cancel`
- [x] Conversation 查询与命令、Agent close/retire 入口
- [x] `GET /healthz`、`GET /readyz`、`POST /admin/drain`
- [x] 有界 delivery bridge：Protected 溢出直接失败该消费者流，Coalescable 可丢（`events.go:159`）
- [x] SSE 事件携带路由元数据 conversation_id 与 agent_generation（`host/server.go:232`）
- [x] SSE 断连行为的断言测试（`host/lifecycle_test.go:85`、`:121`）

## 端点清单

| 方法与路径 | 用途 |
| --- | --- |
| `GET /healthz` | 存活 |
| `GET /readyz` | 就绪，drain 后为 false |
| `POST /v1/runs` | 同步提交一次 Run |
| `POST /v1/runs/async` | 非阻塞提交，返回 run_id |
| `GET /v1/runs/{run_id}` | 轮询一次 Run 的执行状态与指标 |
| `POST /v1/runs/progress` | ndjson 进度流 |
| `POST /v1/runs/{run_id}/cancel` | 取消一次 Run |
| `POST /v1/runs/stream` | SSE 观察一次 Run 的完整事件投影 |
| `GET /v1/conversations/` | 查询 Conversation 记录 |
| `POST /v1/conversations/` | retire 或 close 一个 Conversation |
| `POST /v1/agents/` | 关闭一个 live Core Agent |
| `POST /admin/drain` | 触发 drain |

这套端点是本地测试与演示用的协议投影，不是最终生产 wire contract，冻结动作在 M13。

## 断连策略

一阶段测试场景 G 已经覆盖：打开 SSE、启动 Run、断开客户端、断言投递停止、断言 Agent 的关闭只按显式策略发生。

默认策略是这三条，`host/lifecycle_test.go` 逐条钉住：

```text
SSE 断开 → 停止远端投递
SSE 断开 → 不自动关闭 Agent
是否取消当前 Run → 由 Server.CancelRunOnDisconnect 决定，默认不取消
```

`gatedModel` 记录 Run Context 有没有被取消，用来区分「只停投递」和「连 Run 一起取消」两种结果。

## 退出条件

```text
curl 可以完成一次完整 Prompt
SSE 能观察到 canonical Event order
SSE 断开不会隐式关闭 Agent
Host delivery settlement 与 Core Run settlement 独立
Protected Event 不被静默丢弃
```

## 验收命令

```bash
go test -race ./host/...
```

本地手工验收：

```bash
go run ./cmd/gotato-agent --addr 127.0.0.1:8787
curl -s -X POST http://127.0.0.1:8787/v1/runs -d '{"agent_name":"default","conversation_key":"local-test","prompt":"hello"}'
```
