# M12 — Conversation 持久化与重启恢复

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 7（Conversation persistence and rehydration） | M10 |

## 目标

把 `SnapshotStore` 从只有内存实现，推到有一个真实可持久化的后端，使进程重启之后 Dormant Conversation 还能被复活。

## 前置

M10 已经把 store 变成唯一的状态权威——rehydrate 从 store 读，Orchestration 不再留第二份。所以这一阶段真的只剩「补齐契约 + 换后端」，不用再动读写路径。

## 交付项

- [x] 后端选型：文件存储。理由见下
- [x] `FileSnapshotStore` 实现，一个 Conversation 一个文件
- [x] `StateVersion` 的 compare-and-swap：版本不前进的写入被拒，内存与文件两个实现行为一致
- [x] 原子提交：先写临时文件再 rename，崩在半路留下的是上一版而不是半个文件
- [x] snapshot 不含 goroutine、channel、lock、Context——它本来就是纯数据，序列化即验证
- [x] 持久化失败不报告退休成功（M10 已有，本阶段沿用）
- [x] `SnapshotStore.List` 让重启后的进程找得到自己该负责的 Conversation
- [x] `Orchestrator.Restore` 重建路由表为 Dormant，Agent 不跟着回来
- [x] 重启后 ConversationID 稳定，AgentID 变新，generation 继续递增
- [x] `CloseConversation` 删除保留状态（M10 已有）
- [x] snapshot 体积上界与超限行为
- [x] `cmd/gotato-agent` 加 `--state-dir`，非空则接文件存储并在监听前恢复路由表

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

| 决策 | 结论 |
| --- | --- |
| 后端选型 | 文件。`docs/00` 第 3 节说基础设施是外部的、可替换的，框架自带的参考实现就不该拉数据库；文件存储零依赖，又能真的证明重启恢复 |
| durable provider 契约 | `SnapshotStore` 加 `List`，Save 变成按 `StateVersion` 的 CAS。要接数据库的自己实现这个接口，上层不知道拿到的是哪个 |
| 定义缺失怎么办 | `Restore` 遇到已经没注册的 AgentName 时报出来而不是装进路由表——没人能服务的路由比一个看得见的缺口更糟 |

## 退出条件

```text
杀掉进程再起来，Dormant Conversation 能被复活并继续对话   ✅
ConversationID 跨重启稳定，AgentID 每次复活都是新的       ✅
并发写入按 StateVersion 拒绝，旧版本覆盖不了新版本        ✅
持久化失败不会被报告成成功退休                            ✅
CloseConversation 之后同名 key 不会复用被丢弃的状态       ✅
snapshot 里不含任何不可恢复的运行时对象                   ✅
```

## 测试

| 用例 | 断言 |
| --- | --- |
| `TestConversationSurvivesAProcessRestart` | 换一个 Orchestrator 只共享磁盘目录，恢复后 ConversationID 不变、generation 加一、transcript 还在 |
| `TestFileStoreRejectsAStaleWrite` | 版本不前进的写入被拒，前进的放行 |
| `TestMemoryStoreRejectsAStaleWrite` | 内存实现行为与文件一致 |
| `TestFileStoreBoundsOneState` | 超出体积上界时拒绝，且不留下文件 |
| `TestRestoreReportsAMissingDefinition` | 定义缺失时报出来，不装进路由表 |

## 测试要求

- 用真实后端跑一遍退休、进程重启、复活的完整链路
- 并发写入同一条记录：断言版本冲突被拒绝
- 后端注入失败：断言退休结果是失败且 live route 保留
- snapshot 序列化往返：断言字段无损、无运行时对象泄漏

## 验收命令

```bash
go test -race ./orchestration/...
```
