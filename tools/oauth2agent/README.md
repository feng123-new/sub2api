# OAuth2Agent

把 ChatGPT / Codex OAuth 凭据转换成 **identity-only Codex Agent Identity** 文件，并可直接导入 Sub2API。

目标流程：

```text
Sub2API / Codex OAuth JSON
        ↓
读取 access_token（仅用于注册阶段）
        ↓
生成 Ed25519 密钥
        ↓
POST /v1/agent/register
        ↓
agent_runtime_id
        ↓
POST /v1/agent/{id}/task/register
        ↓
task_id
        ↓
输出 Agent Identity JSON
        ↓
Sub2API / Codex Responses
```

输出文件**不写入** `access_token`、`refresh_token` 或 `id_token`，只包含运行 Codex Agent Identity 所需的私钥和身份字段。

> Agent Identity 目前仍属于 OpenAI Codex 中的开发中能力，不是稳定公开 API。服务端接口或字段可能变化。本工具按当前公开 Codex 源码行为实现，使用前请先在非关键账号上验证。

## 功能

- 读取 Codex `auth.json`、Sub2API 账号导出 JSON，以及常见嵌套 OAuth JSON。
- OAuth → Ed25519 → Agent Identity → Task 全自动注册。
- 输出 Sub2API 可直接识别的 `auth_mode: agentIdentity` JSON。
- 可输出官方 Codex 风格 identity-only `auth.json`。
- 可从 Sub2API 管理接口直接拉取某个 OAuth 账号并转换。
- 可把生成的 Agent Identity 作为**新账号**推回 Sub2API。
- `verify` 可验证 `/responses`，并可检查 `/conversations` 是否返回 401/403。
- `simulate` 使用纯本地 Mock 跑完整链路，不需要真实 OAuth、不消耗额度。
- 输出文件原子写入，并尽量设置为 `0600`。
- 日志不打印 OAuth token 或 Agent 私钥。

## 安装

Python 3.10+：

```bash
python -m venv .venv
source .venv/bin/activate   # Windows: .venv\Scripts\activate
python -m pip install -e .
```

`PyNaCl` 只在上游返回 `encrypted_task_id` 时用于解密；真实环境建议完整安装依赖。

## 1. 本地 OAuth JSON 转换

```bash
oauth2agent convert ~/.codex/auth.json -o agent.json
```

输出示例：

```json
{
  "auth_mode": "agentIdentity",
  "agent_identity": {
    "agent_runtime_id": "agent-...",
    "agent_private_key": "MC4CAQAwBQYDK2VwBCIEI...",
    "task_id": "...",
    "account_id": "...",
    "chatgpt_user_id": "user-...",
    "email": "user@example.com",
    "plan_type": "pro",
    "chatgpt_account_is_fedramp": false
  }
}
```

需要官方 Codex 风格 `auth.json`：

```bash
oauth2agent convert ~/.codex/auth.json -o ~/.codex/agent-auth.json --format codex
```

## 2. 直接从 Sub2API 拉 OAuth 并转换

Sub2API 当前后台支持账号导出接口 `/api/v1/admin/accounts/data`。工具会在本地内存解析导出的 OAuth，不把 token 写入新文件。

```bash
export SUB2API_BASE_URL='https://sub.example.com'
export SUB2API_ADMIN_API_KEY='your-admin-api-key'

oauth2agent pull \
  --account-id 123 \
  -o account-123-agent.json
```

如果希望转换完成后再作为一个**新 Agent Identity 账号**导回 Sub2API：

```bash
oauth2agent pull \
  --account-id 123 \
  -o account-123-agent.json \
  --push \
  --name 'account-123-agent'
```

默认不会覆盖原 OAuth 账号。建议先验证新 Agent Identity，再决定是否停用/删除旧 OAuth 账号。

## 3. 已有 Agent Identity 文件推入 Sub2API

```bash
oauth2agent push agent.json \
  --base-url 'https://sub.example.com' \
  --admin-api-key 'your-admin-api-key' \
  --name 'my-pro-agent'
```

Sub2API 管理 JWT 也支持：

```bash
export SUB2API_JWT='...'
oauth2agent push agent.json --base-url 'https://sub.example.com'
```

## 4. 验证 Agent Identity

这一步会真实请求 Codex，可能消耗少量额度：

```bash
oauth2agent verify agent.json --check-isolation
```

预期：

```text
Responses: OK
Conversations endpoint: HTTP 403
```

401 或 403 都视为聊天记录权限隔离通过；如果 `/conversations` 返回 2xx，工具会判定隔离检查失败。

## 5. 本地模拟运行

不需要真实凭据：

```bash
python -m oauth2agent simulate
```

Mock 会模拟：

1. 从 Sub2API 导出一个 OAuth 账号；
2. 注册 Agent Identity；
3. 校验 Task 注册签名；
4. 写出 identity-only JSON；
5. 推回 Mock Sub2API；
6. 校验 AgentAssertion；
7. `/responses` 返回 `OK`；
8. `/conversations` 返回 403。

成功会显示：

```text
SIMULATION PASSED
```

## 安全边界

- 输入 OAuth 文件和输出 Agent Identity 文件都属于敏感凭据。
- Agent Identity 虽然不含 OAuth access/refresh token，但**私钥 + agent_runtime_id + task_id 仍可代表该 Agent 调用 Codex**。
- 不要提交真实 OAuth、Agent Identity、管理员 API Key 到 Git。
- `.gitignore` 只能降低误提交风险，不能替代 `git diff --cached` 人工检查。
- `pull --push` 默认创建新账号，不覆盖旧 OAuth 账号，避免转换失败时把唯一可恢复凭据破坏掉。

## 与 Sub2API 的兼容点

当前 Sub2API 能识别：

```text
auth_mode = agentIdentity
agent_runtime_id
agent_private_key (PKCS#8 Base64 Ed25519)
task_id
chatgpt_account_id/account_id
chatgpt_user_id
```

并使用 `Authorization: AgentAssertion ...` 调用 Codex。本项目输出的 `sub2api` 格式与该导入结构对齐。

## 开发

```bash
PYTHONPATH=. pytest
python -m oauth2agent simulate
```
