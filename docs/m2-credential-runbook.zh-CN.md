# AEP M2 Credential 运行手册

M2 为管控服务增加加密 Credential 管理。管理员可以通过管理 API 或 `aepctl` 创建、轮换、禁用、授权和删除 Credential。已登录客户端会话只能发现并解析满足以下条件的条目：已启用、`deliveryMode` 为 `client`，且部署、用户、角色或团队任一授权命中。

`server_only` Credential 仅供服务端使用，Agent 无法列出或解析。成功的解析响应始终包含 `Cache-Control: no-store`；每次解析的允许或拒绝结果都会写入 `credential_resolution_audit`，但不会记录明文密钥。

## 主密钥配置

只有配置且仅配置一个主密钥来源时，服务端才启用 Credential 能力：

- `AEP_CREDENTIAL_MASTER_KEY_BASE64`：Base64 编码的 32 字节 AES-256 密钥。
- `AEP_CREDENTIAL_MASTER_KEY_FILE`：密钥或 keyring 文件的绝对路径。

生产密钥必须使用密码学安全的随机生成器，例如：

```sh
openssl rand -base64 32
```

密钥文件可以直接保存上述 Base64 值。需要轮换主密钥时，使用 JSON keyring，并在旧密文仍存在期间保留旧密钥：

```json
{
  "activeKeyId": "credential-key-2026-09",
  "keys": {
    "credential-key-2026-08": "BASE64_32_BYTE_OLD_KEY",
    "credential-key-2026-09": "BASE64_32_BYTE_ACTIVE_KEY"
  }
}
```

文件 Provider 会在每次加解密操作时重新加载 keyring。新建和轮换使用 `activeKeyId`，已有记录按其保存的 key ID 解密。所有使用旧密钥的 Credential 完成轮换前，不得删除旧密钥。直接替换单一环境变量密钥会导致旧记录无法解密。

默认 Compose 文件内置的密钥只用于本地开发，任何非本地部署都必须覆盖它。

## CLI 操作

启动本地环境并配置管理员认证：

```sh
npm run compose:up
export AEPCTL_PASSWORD=change-this-admin-password
```

创建并授权一个可下发给 Agent 的 API key，避免密钥进入命令历史：

```sh
export AEPCTL_CREDENTIAL_VALUE='provider-secret'
go run ./cmd/aepctl credential create --name provider --service example --delivery-mode agent
unset AEPCTL_CREDENTIAL_VALUE

go run ./cmd/aepctl credential assign \
  --credential-id CREDENTIAL_ID --subject-type user --subject-id USER_ID
```

查询元数据、禁用、轮换并撤销授权：

```sh
go run ./cmd/aepctl credential list
go run ./cmd/aepctl credential show --credential-id CREDENTIAL_ID
go run ./cmd/aepctl credential update --credential-id CREDENTIAL_ID --enabled=false
AEPCTL_CREDENTIAL_VALUE='replacement-secret' go run ./cmd/aepctl credential rotate --credential-id CREDENTIAL_ID
go run ./cmd/aepctl credential assignments
go run ./cmd/aepctl credential revoke --assignment-id ASSIGNMENT_ID
```

密钥已经挂载为文件时使用 `--value-file`。`--value` 仅适合本地自动化，因为它可能出现在进程列表和 shell 历史中。

## 参考 Agent

Node 参考 Agent 在每次使用前同步 Credential 元数据，并仅在真正使用时解析密钥。解析值最多在进程内存中保留 30 秒；服务端过期、轮换、撤销或进程退出时会提前失效。密钥不会写入 SQLite、遥测、日志或命令输出。

```sh
export AEP_USERNAME=agent-user AEP_PASSWORD=agent-password AEP_AGENT_ID=agent-id
export AEP_CREDENTIAL_ID=credential-id
export AEP_CREDENTIAL_URL=https://service.example.test/protected
npm run start --workspace @aep/example-node-agent -- credential
```

解析值仅作为 Bearer 请求头发送。输出只包含 Credential ID、服务名与 HTTP 状态。需要更短缓存时间时可设置正数 `AEP_CREDENTIAL_CACHE_MS`。

## 验证

运行 Credential 专项集成测试：

```sh
npm run test:e2e:m2-control
npm run test:e2e:m2-agent
```

管控与 Agent 场景覆盖授权、server-only 隔离、密文存储、轮换与撤销收敛、重启恢复、审计与 CLI 脱敏，以及 SQLite、遥测和进程输出不包含 Credential 明文。
