# Codex Rust HTTP/SSE 出站（阶段一）

这是一个**默认关闭**的出站传输试验。Sub2API 的账号、OAuth Token 生命周期、负载调度、SSE 解析及计费仍由 Go 处理；只有明确列入灰度名单的 Codex HTTPS 请求通过共享 Unix socket 交由 Rust 进程建立真实的上游网络连接。

Rust 使用与 Codex 项目相同的主要 HTTP 库组合（`reqwest 0.12`、`rustls`、`hyper`）。依赖相同不保证 JA3/JA4、ALPN、HTTP/2 帧参数、代理链路或上游看到的全部请求字段完全一致。请用可控的测试端点实测 TLS/HTTP 行为，不要把这项改造描述为官方客户端身份验证。

## 路由范围与回滚

- 默认 `GATEWAY_OPENAI_CODEX_RUST_TRANSPORT_ENABLED=false`；仅设为 `true` 仍不会路由任何账号。
- 必须同时设置 `GATEWAY_OPENAI_CODEX_RUST_TRANSPORT_ACCOUNT_IDS=123,456`，只匹配列表中的正整数账号 ID。
- 目标固定为 `https://chatgpt.com/backend-api/codex/...`，其它 Provider、API Key 平台地址、OpenAI Harvest 和 WebSocket 仍走原有 Go 路径。
- 命中白名单的 OAuth HTTP/SSE 请求优先走 Rust，即使 OAuth outbound 插件正在灰度；未命中的账号仍沿用原有插件。Codex models GET 探测也遵循这一选择。
- Rust 不自动重试已发送的请求，也不跟随 HTTP 重定向。连接异常在 Go 侧标记为可能已发送，以阻止重复执行。
- **即时回滚：**将 `GATEWAY_OPENAI_CODEX_RUST_TRANSPORT_ENABLED=false` 或清空账号列表，重建/重启 Sub2API 容器。Rust sidecar 可以继续运行，不会影响未命中的流量。

## Docker Compose 开发部署

在仓库 `deploy/` 目录：

```sh
docker compose --profile codex-rust build codex-transport-rs
docker compose --profile codex-rust up -d codex-transport-rs
docker compose --profile codex-rust ps codex-transport-rs
```

Rust 服务不映射任何公网端口；只通过 `codex_rust_socket` 挂载的 Unix socket 与 Go 进程交互。Rust 进程收到的目标 URL 仍会被严格限制到 Codex HTTPS 路径，`Proxy-URL` 原样传给 reqwest 的代理实现。请保持 Unix socket 目录仅对可信容器可见。

可使用 `codex-transport-rs/smoke.mjs` 对运行中的 socket 执行不含凭据、不访问外网的 `/health` 和不安全目标拒绝测试。追加 `--upstream-probe` 后，会使用**故意无效**的 Bearer Token 向 Codex models 发出一次 HTTPS GET；测试只核对是否到达上游及返回的 HTTP 协议，401 等鉴权失败属于预期。需在有 Node.js 且能挂载该 socket 的容器中执行，不要把真实账号 Token 写进测试参数或日志。

建议先用虚拟账号 ID 做本地 Unix socket 测试，再选择一枚真实的低流量账号进行灰度；验证成功后逐步扩大列表。**不要在未通过测试时直接启用所有账号。**

## 与 VPS 的既有部署

VPS 的生产配置使用 `/opt/sub2api-deploy/docker-compose.yml` 和 `./runtime/sub2api` 二进制挂载，因此发布时应使用 `deploy/docker-compose.codex-rust.vps-example.yml` 作为单独的 `docker-compose.codex-rust.yml` 覆盖配置。先构建 Rust 镜像、再构建并替换 Go 二进制、保留旧二进制备份、最后用 `docker compose -f docker-compose.yml -f docker-compose.codex-rust.yml ...` 仅重建相关容器。不要覆盖已有 `.env`、PostgreSQL 或 Redis 数据。

测试至少应覆盖：默认关闭、账号白名单、错误时不自动重放、HTTP/2 和 HTTP/1.1、响应状态与 SSE 流、上游代理（直连、HTTP CONNECT、SOCKS）、请求取消、socket 进程退出以及 `resp.Body.Close()` 后资源回收。Rust 镜像构建在隔离环境完成后，再做生产灰度。

本阶段不替换原生 WebSocket 的 TLS 握手，也不承诺与某一版官方 Codex 的 JA3/JA4 字节级一致。reqwest/rustls 的依赖版本和 HTTP/2 协商结果应在每次升级时重新核对。
