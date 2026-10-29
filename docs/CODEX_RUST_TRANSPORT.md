# Codex Rust HTTP/SSE 出站（阶段一）

这是一个**默认关闭**的出站传输试验。Sub2API 的账号、OAuth Token 生命周期、负载调度、SSE 解析及计费仍由 Go 处理；只有明确列入灰度名单的 Codex HTTPS 请求通过共享 Unix socket 交由 Rust 进程建立真实的上游网络连接。

Rust 使用与 Codex 项目相同的主要 HTTP 库组合（`reqwest 0.12`、系统 TLS/OpenSSL、可选 `rustls`、`hyper`）。默认使用 `CODEX_RUST_TLS_BACKEND=native`（Linux 下系统 TLS），并启用 reqwest 的 `native-tls-alpn`，以便与支持 HTTP/2 的服务端协商 `h2`；可设为 `rustls` 回滚。官方 Codex 默认使用系统 TLS 后端，发生特定协商失败后才可能切换到 rustls；本 sidecar 不会在可能已发送请求时自动重放。依赖相同不保证 JA3/JA4、ALPN、HTTP/2 帧参数、代理链路或上游看到的全部请求字段完全一致。

## 路由范围与回滚

- 默认 `GATEWAY_OPENAI_CODEX_RUST_TRANSPORT_ENABLED=false`；仅设为 `true` 仍不会路由任何账号。
- 必须同时设置 `GATEWAY_OPENAI_CODEX_RUST_TRANSPORT_ACCOUNT_IDS=123,456`，只匹配列表中的正整数账号 ID。
- 目标固定为 `https://chatgpt.com/backend-api/codex/...`，其它 Provider、API Key 平台地址、OpenAI Harvest 和 WebSocket 仍走原有 Go 路径。
- 命中白名单的 OAuth HTTP/SSE 请求优先走 Rust，即使 OAuth outbound 插件正在灰度；未命中的账号仍沿用原有插件。Codex models GET 探测也遵循这一选择。
- Rust 不自动重试已发送的请求，也不跟随 HTTP 重定向。连接异常在 Go 侧标记为可能已发送，以阻止重复执行。
- Go 通过仅供 IPC 使用的控制头传递已知的 JSON Content-Length，Rust 在上游 POST 中保留该长度；未知长度的流式请求不伪造长度。控制头本身不会发送到上游。
- **即时回滚：**将 `GATEWAY_OPENAI_CODEX_RUST_TRANSPORT_ENABLED=false` 或清空账号列表，重建/重启 Sub2API 容器。Rust sidecar 可以继续运行，不会影响未命中的流量。

## Docker Compose 开发部署

在仓库 `deploy/` 目录：

```sh
docker compose --profile codex-rust build codex-transport-rs
docker compose --profile codex-rust up -d codex-transport-rs
docker compose --profile codex-rust ps codex-transport-rs
```

Rust 服务不映射任何公网端口；只通过 `codex_rust_socket` 挂载的 Unix socket 与 Go 进程交互。Rust 进程收到的目标 URL 仍会被严格限制到 Codex HTTPS 路径，`Proxy-URL` 原样传给 reqwest 的代理实现。请保持 Unix socket 目录仅对可信容器可见。

只读 TLS ClientHello 对照可使用 `tools/codex_tls_probe_proxy.py`：在受控主机/容器网络启动临时 HTTP CONNECT 捕获代理，然后用虚拟账号 ID 及无效 Bearer Token 指向这个测试代理。它只接受到 `chatgpt.com:443` 的 CONNECT，解析 ClientHello 的 ALPN、密码套件、扩展顺序、supported groups 和 JA3 后立刻结束，绝不转发上游流量或查看实际请求。跨 Windows 和 Linux 的结果只能作为平台差异参考；JA3 对 TLS 扩展顺序敏感，不能作为单一兼容性判据。

可使用 `codex-transport-rs/smoke.mjs` 对运行中的 socket 执行不含凭据、不访问外网的 `/health` 和不安全目标拒绝测试。追加 `--upstream-probe` 后，会使用**故意无效**的 Bearer Token 向 Codex models 发出一次 HTTPS GET；测试只核对是否到达上游及返回的 HTTP 协议，401 等鉴权失败属于预期。需在有 Node.js 且能挂载该 socket 的容器中执行，不要把真实账号 Token 写进测试参数或日志。

建议先用虚拟账号 ID 做本地 Unix socket 测试，再选择一枚真实的低流量账号进行灰度；验证成功后逐步扩大列表。**不要在未通过测试时直接启用所有账号。**

## 与 VPS 的既有部署

VPS 的生产配置使用 `/opt/sub2api-deploy/docker-compose.yml` 和 `./runtime/sub2api` 二进制挂载，因此发布时应使用 `deploy/docker-compose.codex-rust.vps-example.yml` 作为单独的 `docker-compose.codex-rust.yml` 覆盖配置。先构建 Rust 镜像、再构建并替换 Go 二进制、保留旧二进制备份、最后用 `docker compose -f docker-compose.yml -f docker-compose.codex-rust.yml ...` 仅重建相关容器。不要覆盖已有 `.env`、PostgreSQL 或 Redis 数据。

测试至少应覆盖：默认关闭、账号白名单、错误时不自动重放、HTTP/2 和 HTTP/1.1、响应状态与 SSE 流、上游代理（直连、HTTP CONNECT、SOCKS）、请求取消、socket 进程退出以及 `resp.Body.Close()` 后资源回收。Rust 镜像构建在隔离环境完成后，再做生产灰度。

本阶段不替换原生 WebSocket 的 TLS 握手，也不承诺与某一版官方 Codex 的 JA3/JA4 字节级一致。reqwest/rustls 的依赖版本和 HTTP/2 协商结果应在每次升级时重新核对。

## Go/Rust 路由请求级可观测性

Go 网关现在对固定目标 `chatgpt.com/backend-api/codex/*` 的 OpenAI/Codex HTTP 请求，在实际选择传输层之后输出两类结构化 INFO 事件：

- `codex_transport_request`：每次请求拿到响应头或传输失败时记录 `attempt_id`（随机关联 ID）、`route`（`go` / `rust`）、`account_id`、`method`、`endpoint`（固定枚举）、`status_code`、`error_type`、`upstream_protocol`（`h2` / `http1` / `unknown`）、`header_latency_ms`。
- `codex_transport_stream_end`：响应体关闭时，使用同一 `attempt_id`，记录 `finish_reason`、最终 `error_type`、`read_error_type`、`close_error`（布尔值）、`total_duration_ms` 和实际路由、协议；因此 SSE 的完整流时长与响应头耗时分开。

`error_type` 为受限类别，包括 `none`、`http_429`、`http_4xx`、`http_5xx`、`canceled`、`timeout`、`transport`、`rust_ipc`、`rust_connect_failed`、`rust_timeout`、`rust_body_failed`、`rust_request_failed`、`rust_send_failed`、`stream_read_error` / `stream_close_error`，以及 `upstream_response_failed` / `upstream_response_incomplete` / `upstream_response_canceled` / `stream_handler_error`。网络失败无法确认实际 HTTP 协议时记录 `unknown`。Rust sidecar 的上游错误以安全枚举通过私有 IPC 返回 Go。

SSE 处理器会把已接受的 `response.completed`、`response.failed`、`response.incomplete` 或 `[DONE]` 终止状态报告给观测层。若底层 `Read` 在同一次调用返回了完整的终止事件和非 EOF 错误，服务可以正确完成解析，此时 `finish_reason=terminal_completed` / `error_type=none`，底层异常仍在独立的 `read_error_type` 字段中保留（`unexpected_eof`、`canceled`、`timeout`、`connection_closed`、`read_after_close`、`other` 或 `none`），避免把成功请求计入失败率。未接受到终止事件的读取失败仍记录为 `stream_read_error`。调用方须关闭响应体以写出最终事件。

事件不包含上游 URL/查询参数、请求体、响应体、Token 或代理地址。两类事件均设置 `ops_system_log_skip=true`，保留在应用的标准日志输出中，**不会为每条 Codex 请求向 PostgreSQL `ops_system_logs` 增加一条持久化记录**，避免加剧该表的历史索引膨胀。运行时需要启用 INFO 日志级别，并在受控的应用日志中按 `codex_transport_request` / `codex_transport_stream_end` 或 `attempt_id` 检索。

记录范围覆盖经 `HTTPUpstream.Do` 与 `DoWithTLS` 的固定 Codex HTTP/SSE 请求；不包含目标主机预校验阶段被拒绝的请求，也不覆盖原生 WebSocket 连接。全局 Rust 开关和账号 OAuth 条件仍由现有路由逻辑决定，日志显示每次实际选中的 `go` 或 `rust`，不会仅根据配置推断。
