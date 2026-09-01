# M12 — Conversation 持久化与重启恢复

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 未开始 | Slice 7（Conversation persistence and rehydration） | M10 |

## 目标

把 `SnapshotStore` 从只有内存实现，推到有一个真实可持久化的后端，使进程重启之后 Dormant Conversation 还能被复活。

## 现状

`SnapshotStore` 接口已经存在，唯一实现是 `MemorySnapshotStore`（`orchestration/orchestrator.go:69`）。进程一停，所有 Conversation 快照即刻消失，代码里没有任何标记提示这件事。

一阶段明确不宣称崩溃恢复，这个阶段才把它变成能力。

## 交付项

- [ ] 选定持久化后端，并说明为什么是它
- [ ] 落一个 `SnapshotStore` 实现，接口不变
- [ ] `StateVersion` 的 compare-and-swap 语义：并发写入按版本拒绝，不得覆盖新版本
- [ ] snapshot 格式带版本字段，且不含 goroutine、channel、lock、Context 或不可恢复的 provider client
- [ ] 持久化失败的行为：不得报告退休成功，默认保留 live route
- [ ] 进程重启后按 ConversationKey 或 ConversationID 复活 Dormant Conversation
- [ ] 重启后 ConversationID 保持稳定，AgentID 变新，generation 继续递增
- [ ] `CloseConversation` 删除保留的 snapshot，之后同名 key 不得静默复用被丢弃的状态
- [ ] snapshot 体积上界与超限行为

## 边界

这个阶段做的是单进程重启恢复，不是跨进程放置。以下仍属 Reserved，不在本阶段承诺：

```text
跨进程 Agent placement
多 Pod Conversation 连续性
durable Run 与 Run 级恢复
远程 Agent Routine
可重连、可续传的事件投递
```

普通负载均衡不是连续性保证。

## 需要先定的事

| 决策 | 说明 |
| --- | --- |
| 后端选型 | 文件、嵌入式 KV 还是外部数据库，影响部署形态 |
| durable state provider 契约与失败行为 | 接口要不要为未来的外部存储留位 |
| snapshot 兼容策略 | 老版本 snapshot 遇到新版本代码怎么办 |

## 退出条件

```text
杀掉进程再起来，Dormant Conversation 能被复活并继续对话
ConversationID 跨重启稳定，AgentID 每次复活都是新的
并发写入按 StateVersion 拒绝，旧版本覆盖不了新版本
持久化失败不会被报告成成功退休
CloseConversation 之后同名 key 不会复用被丢弃的状态
snapshot 里不含任何不可恢复的运行时对象
```

## 测试要求

- 用真实后端跑一遍退休、进程重启、复活的完整链路
- 并发写入同一条记录：断言版本冲突被拒绝
- 后端注入失败：断言退休结果是失败且 live route 保留
- snapshot 序列化往返：断言字段无损、无运行时对象泄漏

## 验收命令

```bash
go test -race ./orchestration/...
```
