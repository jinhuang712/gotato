# M0 — Library 骨架与依赖方向

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 1（包边界部分） | 无 |

## 目标

先立住 Library 的包边界与依赖方向，再往里填语义。这个阶段不产出可执行服务：服务只能在 Library 契约稳定之后组装。

## 交付项

- [x] `go.mod`，module `github.com/jinhuang712/gotato`，Go 1.22
- [x] Core 公共类型与接口：`types.go`、`model.go`、`tool.go`、`events.go`、`errors.go`、`limits.go`
- [x] Model 契约：`Model` / `ModelStream` / `ModelRequest` / `ModelEvent`（`model.go`）
- [x] Tool 契约：`Tool` / `ToolSpec` / `ToolUse`（`tool.go`）
- [x] Event 契约：`Event` / `EventClass` / `EventKind`（`events.go:11`）
- [x] `orchestration` 与 `host` 的包边界成立
- [x] 最小 package 级编译测试

## 退出条件

```text
go vet ./... 通过
go test ./... 通过
Core 包不 import host、orchestration、net/http、任何 provider SDK
```

## 验收命令

```bash
go vet ./...
go test ./...
```

## 遗留

无。依赖方向的持续守卫写在本目录 README 的结构不变量一节，每个后续阶段都要对一遍。
