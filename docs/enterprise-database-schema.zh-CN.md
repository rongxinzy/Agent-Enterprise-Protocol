# 企业版数据库表结构与索引

本文档描述 AEP Control Service 当前企业版数据层（PostgreSQL）。内容以版本化
迁移为准，`services/control-service/internal/db/schema.sql` 是便于检查和新环境
初始化的结构快照，不应替代迁移文件作为生产升级来源。

## 范围与版本

- 数据库：PostgreSQL（`timestamptz`、`jsonb`、数组和 `bytea`）。
- 迁移：`001_init.sql` 至 `006_model_reasoning.sql`，由服务启动时幂等执行。
- 业务表：21 张。
- 运行时表：`schema_migrations` 1 张，因此已完成全部迁移的数据库通常有 22 张表。
- 直接带有 `enterprise_id` 的业务表以该字段作为租户边界；其余表通过企业根实体的
  外键链路或脱敏键参与隔离。复合外键用于防止跨企业引用。

> **快照漂移提示**：当前 `schema.sql` 只包含 19 张 `CREATE TABLE`，尚未同步
> `004_data_plane.sql` 的 `data_plane_desired_states` 和 `data_plane_statuses`。
> 因此生产环境和集成测试必须执行全部版本化迁移；后续应补一个仅同步快照的 PR，
> 不要通过修改已执行的迁移来修复这个差异。

## 表结构

字段标记：`PK` 主键，`FK` 外键，`UQ` 唯一约束，`NN` 非空，`DF` 默认值，`CK` 检查约束。

### 1. `enterprises`

企业租户根实体。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `name` | `text` | NN |
| `created_at` | `timestamptz` | NN, DF `now()` |

### 2. `organizations`

企业内组织树。`parent_id` 可为空，指向同表组织。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `enterprise_id` | `text` | NN, FK -> `enterprises.id` |
| `name` | `text` | NN |
| `parent_id` | `text` | FK -> `organizations.id`，可空 |
| `created_at` | `timestamptz` | NN, DF `now()` |

### 3. `users`

企业用户、管理员和密码认证主体。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `enterprise_id` | `text` | NN, FK -> `enterprises.id` |
| `username` | `text` | NN；与 `enterprise_id` 组成 UQ |
| `display_name` | `text` | NN |
| `email` | `text` | 可空 |
| `password_hash` | `text` | NN；保存 Argon2id 哈希，不保存明文 |
| `status` | `text` | NN, DF `active`；CK `active`/`disabled` |
| `require_password_change` | `boolean` | NN, DF `true` |
| `is_admin` | `boolean` | NN, DF `false` |
| `organization_ids` | `text[]` | NN, DF `'{}'`；当前为组织 ID 快照数组 |
| `role_ids` | `text[]` | NN, DF `'{}'`；角色 ID 快照数组 |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### 4. `agents`

客户端安装实例。一个用户可以绑定多个 Agent，`agent_id` 是实例稳定标识而非用户 ID。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `agent_id` | `text` | PK |
| `enterprise_id` | `text` | NN, FK -> `enterprises.id` |
| `user_id` | `text` | NN, FK -> `users.id` |
| `agent_version` | `text` | NN |
| `platform` | `text` | NN；CK `windows`/`macos`/`linux` |
| `first_seen_at` | `timestamptz` | NN, DF `now()` |
| `last_seen_at` | `timestamptz` | NN, DF `now()` |
| `applied_skill_revision` | `text` | 可空 |
| `installed_skill_ids` | `text[]` | NN, DF `'{}'` |

### 5. `refresh_sessions`

Refresh Token 会话。数据库只保存 Token 哈希；轮换或退出时设置 `revoked_at`。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `token_hash` | `text` | PK |
| `enterprise_id` | `text` | NN, FK -> `enterprises.id` |
| `user_id` | `text` | NN, FK -> `users.id` |
| `agent_id` | `text` | NN, FK -> `agents.agent_id` |
| `expires_at` | `timestamptz` | NN |
| `revoked_at` | `timestamptz` | 可空 |
| `created_at` | `timestamptz` | NN, DF `now()` |

### 6. `skills`

Skill 逻辑资源及启用状态。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `name` | `text` | NN |
| `description` | `text` | NN, DF `''` |
| `enabled` | `boolean` | NN, DF `true` |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### 7. `skill_versions`

Skill ZIP 版本和对象存储元数据。文件本体在 MinIO，数据库保存地址和完整性摘要。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `skill_id` | `text` | PK 组成列，NN, FK -> `skills.id` ON DELETE CASCADE |
| `version` | `text` | PK 组成列，NN |
| `object_key` | `text` | NN；MinIO 对象键 |
| `sha256` | `text` | NN；ZIP SHA-256 |
| `size_bytes` | `bigint` | NN |
| `published` | `boolean` | NN, DF `false` |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `published_at` | `timestamptz` | 可空 |

### 8. `skill_assignments`

Skill 对企业、组织、用户或 Agent 的授权并集。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `enterprise_id` | `text` | NN, FK -> `enterprises.id` |
| `skill_id` | `text` | NN, FK -> `skills.id` ON DELETE CASCADE |
| `subject_type` | `text` | NN；CK `enterprise`/`organization`/`user`/`agent` |
| `subject_id` | `text` | NN；由 `subject_type` 解释 |
| `created_at` | `timestamptz` | NN, DF `now()` |

UQ：`(enterprise_id, skill_id, subject_type, subject_id)`。

### 9. `credentials`

企业凭证密文和轮换元数据。M0 仅支持 API Key 类型。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `enterprise_id` | `text` | PK 组成列，NN, FK -> `enterprises.id` |
| `id` | `text` | PK 组成列，NN |
| `name` | `text` | NN |
| `service` | `text` | NN |
| `type` | `text` | NN；CK `api_key` |
| `delivery_mode` | `text` | NN；CK `server_only`/`agent` |
| `encrypted_value` | `bytea` | NN；应用层加密密文 |
| `nonce` | `bytea` | NN；对应加密随机数 |
| `key_id` | `text` | NN；外部密钥环引用 |
| `masked_value` | `text` | NN；展示用脱敏值 |
| `enabled` | `boolean` | NN, DF `true` |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |
| `rotated_at` | `timestamptz` | NN, DF `now()` |

### 10. `credential_assignments`

凭证对企业、组织、用户或 Agent 的授权。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `enterprise_id` | `text` | NN；复合 FK 的组成列 |
| `credential_id` | `text` | NN；复合 FK 的组成列 |
| `subject_type` | `text` | NN；CK `enterprise`/`organization`/`user`/`agent` |
| `subject_id` | `text` | NN |
| `created_at` | `timestamptz` | NN, DF `now()` |

复合 FK：`(enterprise_id, credential_id)` -> `credentials`，删除凭证时级联删除授权。
UQ：`(enterprise_id, credential_id, subject_type, subject_id)`。

### 11. `credential_resolution_audit`

凭证解析/下发的审计记录，不保存凭证明文。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `enterprise_id` | `text` | NN, FK -> `enterprises.id` |
| `credential_id` | `text` | NN；业务层校验同租户 |
| `user_id` | `text` | NN, FK -> `users.id` |
| `agent_id` | `text` | NN, FK -> `agents.agent_id` |
| `purpose` | `text` | NN |
| `outcome` | `text` | NN；CK `resolved`/`denied` |
| `reason` | `text` | 可空 |
| `created_at` | `timestamptz` | NN, DF `now()` |

### 12. `models`

企业模型目录。实际请求通过模型的 OpenAI-compatible endpoint 转发到网关或本地模型。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `enterprise_id` | `text` | PK 组成列，NN, FK -> `enterprises.id` |
| `id` | `text` | PK 组成列，NN |
| `display_name` | `text` | NN |
| `source_type` | `text` | NN；CK `gateway`/`enterprise_open_source`/`local` |
| `protocol` | `text` | NN；CK `openai-compatible` |
| `endpoint` | `text` | 可空；网关 Base URL |
| `upstream_model` | `text` | 可空；上游模型名 |
| `local_model_ref` | `text` | 可空；本地模型引用 |
| `credential_id` | `text` | 可空；同企业凭证复合 FK |
| `capabilities` | `text[]` | NN, DF `'{}'` |
| `reasoning_compatibility` | `jsonb` | 可空；必须为 JSON object |
| `context_window` | `integer` | 可空；CK 大于 0 |
| `is_default` | `boolean` | NN, DF `false`；每企业最多一个默认模型 |
| `enabled` | `boolean` | NN, DF `true` |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

复合 FK：`(enterprise_id, credential_id)` -> `credentials`，删除凭证受 `RESTRICT` 保护。

### 13. `model_assignments`

模型对企业、组织、用户或 Agent 的授权。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `enterprise_id` | `text` | NN |
| `model_id` | `text` | NN |
| `subject_type` | `text` | NN；CK `enterprise`/`organization`/`user`/`agent` |
| `subject_id` | `text` | NN |
| `created_at` | `timestamptz` | NN, DF `now()` |

复合 FK：`(enterprise_id, model_id)` -> `models` ON DELETE CASCADE。
UQ：`(enterprise_id, model_id, subject_type, subject_id)`。

### 14. `control_events`

待下发的管控事件及其作用域。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `event_id` | `text` | PK；幂等事件标识 |
| `enterprise_id` | `text` | NN, FK -> `enterprises.id` |
| `type` | `text` | NN |
| `scope_type` | `text` | NN；CK `global`/`organization`/`user`/`agent` |
| `scope_id` | `text` | 可空；global 时通常为空 |
| `resource_type` | `text` | 可空 |
| `resource_id` | `text` | 可空 |
| `resource_revision` | `text` | 可空 |
| `task_type` | `text` | NN |
| `supersedes_key` | `text` | 可空；替代同类事件 |
| `state` | `text` | NN, DF `active` |
| `expires_at` | `timestamptz` | NN |
| `created_by` | `text` | NN, FK -> `users.id` |
| `created_at` | `timestamptz` | NN, DF `now()` |

### 15. `control_deliveries`

事件到具体 Agent 的独立投递状态，可重试、接收、执行并上报结果。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `cursor` | `bigserial` | UQ；按投递顺序读取 |
| `delivery_id` | `text` | PK |
| `event_id` | `text` | NN, FK -> `control_events.event_id` ON DELETE CASCADE |
| `agent_id` | `text` | NN, FK -> `agents.agent_id` |
| `state` | `text` | NN, DF `pending` |
| `attempt_count` | `integer` | NN, DF `0` |
| `received_at` | `timestamptz` | 可空 |
| `started_at` | `timestamptz` | 可空 |
| `completed_at` | `timestamptz` | 可空 |
| `applied_revision` | `text` | 可空 |
| `error_code` | `text` | 可空 |
| `message` | `text` | 可空 |
| `updated_at` | `timestamptz` | NN, DF `now()` |

UQ：`(event_id, agent_id)`，保证同一事件对同一 Agent 只有一条投递。

### 16. `telemetry_events`

Agent 遥测和结果事件。`event_id` 主键同时提供服务端去重语义。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `event_id` | `text` | PK；重复上报幂等 |
| `enterprise_id` | `text` | NN, FK -> `enterprises.id` |
| `user_id` | `text` | NN, FK -> `users.id` |
| `agent_id` | `text` | NN, FK -> `agents.agent_id` |
| `type` | `text` | NN |
| `resource_type` | `text` | 可空 |
| `resource_id` | `text` | 可空 |
| `result` | `text` | 可空 |
| `payload` | `jsonb` | NN, DF `'{}'` |
| `occurred_at` | `timestamptz` | NN；Agent 发生时间 |
| `received_at` | `timestamptz` | NN, DF `now()` |

### 17. `skill_sync_results`

Agent Skill 期望状态收敛结果和安装摘要。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `enterprise_id` | `text` | NN, FK -> `enterprises.id` |
| `user_id` | `text` | NN, FK -> `users.id` |
| `agent_id` | `text` | NN, FK -> `agents.agent_id` |
| `revision` | `text` | NN |
| `status` | `text` | NN；业务层定义同步结果 |
| `installed_skill_ids` | `text[]` | NN, DF `'{}'` |
| `payload` | `jsonb` | NN, DF `'{}'` |
| `created_at` | `timestamptz` | NN, DF `now()` |

### 18. `data_plane_desired_states`

企业模型网关/数据平面的期望路由状态，每企业一条当前记录。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `enterprise_id` | `text` | PK, FK -> `enterprises.id` ON DELETE CASCADE |
| `revision` | `text` | NN |
| `routes` | `jsonb` | NN；路由配置 |
| `content_hash` | `text` | NN；CK 为 64 位小写十六进制 SHA-256 |
| `published_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### 19. `data_plane_statuses`

企业数据平面实际观测状态，每企业一条当前记录。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `enterprise_id` | `text` | PK, FK -> `enterprises.id` ON DELETE CASCADE |
| `state` | `text` | NN；CK `pending`/`applying`/`ready`/`degraded`/`error` |
| `observed_revision` | `text` | 可空 |
| `content_hash` | `text` | 可空；非空时为 64 位小写十六进制 SHA-256 |
| `last_applied_at` | `timestamptz` | 可空 |
| `error_code` | `text` | 可空 |
| `message` | `text` | 可空 |
| `resource_count` | `integer` | NN, DF `0`；CK 大于等于 0 |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### 20. `login_rate_limits`

登录失败限流状态。`key_hash` 是脱敏后的限流键，不保存原始凭据或来源标识。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `key_hash` | `text` | PK |
| `failure_count` | `integer` | NN；CK 大于 0 |
| `blocked_until` | `timestamptz` | 可空 |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### 21. `authentication_audit_events`

认证成功、失败、限流和密码变更审计。哈希字段用于检索关联但不暴露敏感值。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `cursor` | `bigserial` | PK；单调审计游标 |
| `enterprise_id` | `text` | NN；业务层租户字段 |
| `user_id` | `text` | 可空；失败请求可能没有有效用户 |
| `agent_id` | `text` | NN；业务层 Agent 字段 |
| `event_type` | `text` | NN；CK `login.succeeded`/`login.failed`/`login.throttled`/`password.changed` |
| `outcome` | `text` | NN；CK `success`/`failure`/`denied` |
| `reason` | `text` | 可空 |
| `principal_hash` | `text` | NN；主体脱敏哈希 |
| `source_hash` | `text` | NN；来源脱敏哈希 |
| `created_at` | `timestamptz` | NN, DF `now()` |

### 22. `schema_migrations`（运行时表）

迁移器自动创建的版本跟踪表，不属于企业业务域。每个迁移文件成功执行后写入一行。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `version` | `text` | PK；迁移文件名，例如 `001_init.sql` |
| `applied_at` | `timestamptz` | NN, DF `now()` |

## 显式索引

以下索引由迁移显式创建（`IF NOT EXISTS`，可重复执行）：

| 索引 | 表 | 定义 | 主要用途 |
| --- | --- | --- | --- |
| `idx_agents_user` | `agents` | `(enterprise_id, user_id)` | 查询某企业用户绑定的全部 Agent |
| `idx_deliveries_agent_state` | `control_deliveries` | `(agent_id, state, cursor)` | 按 Agent 拉取待处理/可重试投递并保持游标顺序 |
| `idx_telemetry_search` | `telemetry_events` | `(enterprise_id, agent_id, occurred_at DESC)` | 管理查询按企业、Agent 和时间倒序检索遥测 |
| `idx_models_default` | `models` | 唯一部分索引 `(enterprise_id) WHERE is_default` | 保证每企业最多一个默认模型 |
| `idx_model_assignments_subject` | `model_assignments` | `(enterprise_id, subject_type, subject_id, model_id)` | 按授权主体解析可用模型 |
| `idx_credential_assignments_subject` | `credential_assignments` | `(enterprise_id, subject_type, subject_id, credential_id)` | 按授权主体解析可用凭证 |
| `idx_credential_resolution_audit` | `credential_resolution_audit` | `(enterprise_id, credential_id, created_at DESC)` | 查询凭证解析审计时间线 |
| `idx_login_rate_limits_updated` | `login_rate_limits` | `(updated_at)` | 清理过期限流记录 |
| `idx_authentication_audit_enterprise_time` | `authentication_audit_events` | `(enterprise_id, created_at DESC)` | 管理员按企业查询认证审计 |

### PostgreSQL 自动索引

主键和 `UNIQUE` 约束会由 PostgreSQL 自动创建唯一索引，包括用户登录名、Skill/模型/凭证
授权去重、Skill 版本复合主键和事件到 Agent 投递去重等。外键约束不会自动创建索引，
因此当前显式索引只覆盖已验证的读取路径。

## 租户隔离与引用规则

- `enterprise_id` 必须来自认证主体，不能由客户端任意提升或替换。
- 模型和凭证使用 `(enterprise_id, id)` 复合主键，关联时同样使用复合 FK，避免跨企业引用。
- `subject_id` 是多态主体 ID，数据库通过 `subject_type` 检查允许的类别；主体是否属于当前企业由服务层校验。
- `authentication_audit_events` 的主体字段保持非 FK，以便记录已删除用户或异常登录请求；查询端仍按企业边界过滤。
- 删除策略由 FK 的 `CASCADE`/`RESTRICT` 明确控制：删除 Skill/模型会清理其授权，凭证被模型引用时禁止删除。

## 安全边界

### License 验签公钥

License 验签公钥不存数据库，也不由 Control Service 生成。当前 License envelope
使用 Ed25519；公钥用于验证签名完整性和签发方，不用于解密业务数据。它同时配置到
Control Service 和企业客户端：

- 运行配置：`D:\rxzy\ZhiyuanAaaS\resources\zhiyuan-enterprise\config.json`
- 构建模板：`D:\rxzy\ZhiyuanAaaS\build\enterprise-config.example.json`
- 配置字段：`license.trustedKeys`
- License 文件：同目录的 `license.zylic`
- 服务端配置：`AEP_LICENSE_TRUSTED_KEYS_FILE`、`AEP_LICENSE_FILE`、
  `AEP_LICENSE_CUSTOMER_ID`、`AEP_LICENSE_DEPLOYMENT_ID` 和
  `AEP_LICENSE_ENTERPRISE_ID`
- 离线签名器和生产私钥：仓库外的本地受控目录，例如 `D:\rxzy\zhiyuan-license-signer\`

私钥、签名器源码、签发配置和未脱敏签发日志不得进入 AEP/AaaS 仓库、CI 或服务镜像。

### AEP JWT/model JWT 验签公钥

AEP 会话 JWT 和 model JWT 的公钥由 Control Service 的
`GET /.well-known/jwks.json` 暴露。签名私钥/seed 由部署 Secret 提供，网关通过
`AEP_GATEWAY_JWKS_URL` 获取 JWKS。该公钥不写入数据库或 Git 仓库；数据库只保存会话
Token 哈希和业务审计信息。

## 当前索引评估与后续建议

当前 9 个显式索引已覆盖 M0 的 Agent 拉取、授权解析、遥测和审计查询。随着数据量增长，
建议用真实查询计划（`EXPLAIN (ANALYZE, BUFFERS)`）评估后再增加索引，优先关注：

1. `control_events` 按企业、作用域和过期时间筛选的组合查询。
2. `skill_sync_results` 按 Agent 和创建时间查询的历史页。
3. `refresh_sessions` 按 Agent 或用户清理过期/吊销会话的后台任务。
4. `credential_resolution_audit` 按 Agent 和时间范围查询的运维页面。

新增索引应通过新的编号迁移提交，不能直接修改已执行的迁移或仅修改 `schema.sql`。
