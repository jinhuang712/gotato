# M4 — 进程内 Orchestration

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 5（Embedded Orchestration 的一阶段范围） | M1、M2 |

## 目标

让同一个进程里能按 ConversationKey 反复找回同一个 Agent，能显式退休它、把 Conversation 变成 Dormant，再用新的 AgentID 和递增的 generation 把它复活，全程不产生第二个 live Agent。

## 交付项

- [x] Agent definition 与 factory：`Definition` / `Register`（`orchestration/orchestrator.go:59,141`）
- [x] ConversationKey 到 ConversationRecord 的映射（`orchestration/orchestrator.go:45`）
- [x] live Agent handle retention 与 per-Conversation create-or-resolve 串行化（`Resolve`）
- [x] single-flight dispatch 与 admission 信号（`Dispatch` / `DispatchWithAdmission`）
- [x] 显式 `Retire`，`Retain` 与 `Ephemeral` 两种最小策略（`orchestration/orchestrator.go:358`）
- [x] Conversation 状态机 `Active / Retiring / Dormant / Closed / Archived`
- [x] in-memory snapshot store（`orchestration/orchestrator.go:69`）
- [x] Dormant 记录的 rehydration，新 AgentID 加 generation 递增（`orchestration/orchestrator.go:240`）
- [x] stale generation 与 stale handle fencing（`acquire`，`orchestration/orchestrator.go:339`）
- [x] 退休期间禁止并发 rehydration
- [x] snapshot 或 record commit 失败时不报告退休成功，保留 live route

## 退休顺序

实现必须按这个顺序走，任何一步失败都不得宣布退休成功：

```text
1. 原子地把 Conversation 标记为 Retiring
2. 停止新 admission
3. 等待或显式取消 current Run
4. 在 quiescent boundary 取 Core snapshot
5. 提交带版本的 snapshot record
6. 调用 Agent.Close
7. 移除 live handle
8. 把 Conversation 标记为 Dormant
```

Core close 超时但后台仍在 Closing 时，Conversation 保持 `Retiring`，不得创建新 Agent，由 Host 报告 incomplete retirement。

## 退出条件

```text
同一个 Key 不产生重复 live Agent
Run settlement 之后仍可复用同一个 Agent
Retiring 阶段拒绝新 dispatch
snapshot 失败不会报告成功退休
rehydration 使用新 AgentID
stale generation 不能提交新请求
```

## 测试场景

| 场景 | 内容 | 用例 |
| --- | --- | --- |
| D | resolve key → Prompt → 断言 generation 0 → Retire → 断言 Dormant → 再 resolve → 断言新 AgentID 与 generation 1 → 旧 generation 不能 dispatch | `orchestration/orchestrator_test.go:50` |
| E | 退休后 N 个调用方并发 resolve 同一个 key，断言恰好一个新 live Agent，所有被接受的请求用同一个 generation | `orchestration/orchestrator_test.go:86` |
| F | 让 snapshot store 失败 → Retire → 断言没有成功退休结果、没有 rehydration、路由仍安全可观测 | `orchestration/orchestrator_test.go:145` |

## 验收命令

```bash
go test -race ./orchestration/...
```

## 遗留

`AfterRun` 与 `AfterIdle` 两个策略常量已经声明（`orchestration/orchestrator.go:26`），但没有触发路径。一阶段范围只要求 `Retain`、显式 retirement 和 `Ephemeral`，这两个策略的生效归 M10。
