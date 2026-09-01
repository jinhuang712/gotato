# M3 — gotato-gateway LLM 适配器

| 状态 | 对应 Slice | 依赖 |
| --- | --- | --- |
| 已完成 | Slice 2（LLM 侧） | M0 |

## 目标

用一个 OpenAI 兼容的 HTTP Gateway 适配器实现 Gotato 的 `Model` 接口，把 provider 的 SSE 流归一化成 Core 的 Model Event，同时保证 Core 不出现任何 provider 类型。

## 交付项

- [x] OpenAI 兼容 HTTP Gateway adapter（`gateway/gateway.go`）
- [x] Pi Codex 专用适配器，含 OAuth token 刷新与 reasoning artifact 重放（`gateway/codex.go`）
- [x] YAML 配置加载与环境变量占位符展开（`gateway/config.go`）
- [x] base URL、complete endpoint、API Key、自定义 header 配置
- [x] SSE 解码与 text、usage、finish reason 归一化
- [x] streaming Tool Call 组装与可逆 Tool ID 映射
- [x] 仅在 stream 建立前的有界重试（`gateway/gateway.go:108`）
- [x] Provider HTTP error 独立分类，错误不泄露 API Key
- [x] `httptest` Gateway fixture（`gateway/gateway_test.go`、`gateway/codex_test.go`）

## 退出条件

```text
Core 只依赖 provider-neutral 的 Model 接口
Gateway 请求带正确的认证头、Model、Messages 和 Tools
text、usage、tool call、finish reason 能转成 Core Model Events
429 与 5xx 只在 stream 建立前按次数重试
Gateway 错误不泄露 API Key
```

## 验收命令

```bash
go test -race ./gateway/...
```

## 遗留

`refreshCodexToken` 与 `persistPiCredential` 的落盘路径无用例，`expandPath` 同样无用例。这三处涉及本机凭据文件读写，测试要先造临时目录 fixture，归入 M3 的补测而不阻塞后续阶段。
