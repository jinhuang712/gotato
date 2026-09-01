# M13 — wire contract 冻结与第二协议适配器

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 未开始 | Slice 6 收口 · Slice 7（cross-process adapters） | M7、M11、M12 |

## 目标

把本地测试用的 HTTP/SSE 投影升级成冻结的 wire contract，并用第二个协议适配器证明 Host 的语义边界确实与协议无关。

## 现状

`specs/09-agent-service-and-grpc.md` 以 gRPC 立契约，给出了 `RunCommand` 与 `RunEvent` 的 proto 草案。实现是纯 `net/http` 加 SSE，`go.mod` 里没有 grpc 或 protobuf 依赖。这两者需要先对齐再动手。

现有端点在 M5 的清单里，它们当前的定位是本地测试与演示投影，不是生产契约。

## 交付项

- [ ] 决定 gRPC 是目标协议、备选协议，还是明确取消（对应决策见本目录 README 的待拍板表）
- [ ] 冻结 wire contract：命令集合、事件集合、错误码、版本策略
- [ ] `Start` 是第一条命令，且恰好含一个 Prompt 或 Continue
- [ ] 重复 `Start`、终态之后的命令，都是协议错误
- [ ] `Cancel` 在 Active 期间幂等，终态之后的行为写进文档
- [ ] Agent close 与 retirement 是独立的 Host 生命周期操作，不是 `RunCommand`，也不是流关闭的副作用
- [ ] wire 事件保留 Core 身份、事件分类、correlation、顺序与已结算含义
- [ ] Protobuf 或任何 wire 类型不得进入 Core 签名
- [ ] 第二个协议适配器落地，与 HTTP 适配器共用同一个 Host 语义接口
- [ ] Embedded 与 Hosted 等价性测试：同一个脚本化场景，两条路径产出相同的 canonical Event 序列、相同的 transcript 提交、相同的终态

## 边界

协议流的生命周期与 Agent 的生命周期是两件事：

```text
BeforeStart ── 合法 Start ──► Active
BeforeStart ── 其它命令 ───► 协议错误
Active ────── 终态 ────────► Terminal
Active ────── 流关闭 ──────► 投递流关闭
```

关掉投递流不自动关闭 Agent 或 Conversation，除非 Host 策略显式要求取消 Run 或退休 Agent。close 确认代表 Core 已关闭，该确认的投递可以更晚结算。

## 退出条件

```text
wire contract 有版本与冻结说明
两个协议适配器共用同一个 Host 语义接口，没有第二套 Agent 实现
同一场景在 Embedded 与 Hosted 两条路径产出相同 canonical Event 序列
Protected Event 在 wire 层保序，保不住就让消费者流失败
关闭投递流不会被报告成 Conversation 已关闭
Core 包依旧不 import 任何 wire 类型
```

## 验收命令

```bash
go vet ./...
go test -race ./...
```
