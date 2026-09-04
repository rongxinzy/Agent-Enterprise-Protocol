# 企业版数据库表结构与索引

本文档描述 AEP Control Service 在迁移 `017_license_deployment_index.sql` 完成后的
PostgreSQL 最终结构。生产数据库以
`services/control-service/internal/db/migrations/` 中的版本化迁移为准；
`services/control-service/internal/db/schema.sql` 是 sqlc 使用的当前结构快照，不能替代
生产迁移。

## 当前模型

- 安装边界是 `deployment`。每套客户内网部署通常只有一条 `deployments` 记录。
- 身份与授权主体只有 `user`、`role`、`team`。
- 一个用户可同时属于多个角色和 Team，其中各有一个主绑定。
- 每次客户端登录创建独立 `user_session`；同一用户的多个终端分别持有会话和事件投递游标。
- Skill、Credential、Model 的授权取用户、角色和 Team 授权的并集。
- 当前共有 29 张业务表；迁移器另建 1 张 `schema_migrations`，合计 30 张。
- 旧 `enterprises`、`organizations`、`agents`、`refresh_sessions` 和
  `control_deliveries` 已由迁移 `015_identity_cleanup.sql` 删除。历史迁移中的旧名称必须
  保留，以支持已有数据库顺序升级。

字段标记：`PK` 主键，`FK` 外键，`UQ` 唯一约束，`NN` 非空，`DF` 默认值，`CK` 检查约束。
多数 `deployment_id` 直接引用 `deployments.id`；关联表也可通过复合外键间接限定部署边界。
删除部署时，部署所有的业务数据级联清理。

## 部署、身份与 RBAC

### `deployments`

单套软件安装的业务根，不代表共享 SaaS 租户。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `name` | `text` | NN |
| `created_at` | `timestamptz` | NN, DF `now()` |

### `users`

本地账号、管理员和密码认证主体。角色与 Team 成员关系只存绑定表，不再保存数组快照。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `deployment_id` | `text` | NN, FK |
| `username` | `text` | NN；与 `deployment_id` 组成 UQ |
| `display_name` | `text` | NN |
| `email` | `text` | 可空 |
| `password_hash` | `text` | NN；Argon2id 哈希，不保存明文 |
| `status` | `text` | NN, DF `active`, CK `active`/`disabled` |
| `require_password_change` | `boolean` | NN, DF `true` |
| `is_admin` | `boolean` | NN, DF `false`；兼容引导字段，权限以角色绑定为准 |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### `permissions`

系统级权限目录。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK；稳定权限标识 |
| `description` | `text` | NN, DF `''` |

### `roles`

部署内角色。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `deployment_id` | `text` | PK 组成列，NN, FK |
| `id` | `text` | PK 组成列，NN |
| `name` | `text` | NN；部署内 UQ |
| `description` | `text` | NN, DF `''` |
| `built_in` | `boolean` | NN, DF `false` |
| `enabled` | `boolean` | NN, DF `true` |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### `role_permissions`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `deployment_id` | `text` | PK 组成列，NN |
| `role_id` | `text` | PK 组成列，NN；复合 FK -> `roles` |
| `permission_id` | `text` | PK 组成列，NN, FK -> `permissions.id` |

### `teams`

部署内用户分组；内置 `all-users` Team 表示全体用户。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `deployment_id` | `text` | PK 组成列，NN, FK |
| `id` | `text` | PK 组成列，NN |
| `name` | `text` | NN；部署内 UQ |
| `description` | `text` | NN, DF `''` |
| `built_in` | `boolean` | NN, DF `false` |
| `enabled` | `boolean` | NN, DF `true` |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### `user_role_bindings` 与 `user_team_bindings`

| 表 | 复合主键 | 其他字段/约束 |
| --- | --- | --- |
| `user_role_bindings` | `(deployment_id, user_id, role_id)` | `user_id` FK -> `users`；`(deployment_id, role_id)` 复合 FK -> `roles`；`is_primary boolean` DF `false`；`created_at` |
| `user_team_bindings` | `(deployment_id, user_id, team_id)` | `user_id` FK -> `users`；`(deployment_id, team_id)` 复合 FK -> `teams`；`is_primary boolean` DF `false`；`created_at` |

## 会话与认证

### `user_sessions`

每个登录终端一条记录。`topic` 用于同一用户多终端的事件扇出，不是消息队列 Topic。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `session_id` | `text` | PK |
| `deployment_id` | `text` | NN, FK |
| `user_id` | `text` | NN, FK -> `users.id` |
| `topic` | `text` | NN |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `last_seen_at` | `timestamptz` | NN, DF `now()` |
| `revoked_at` | `timestamptz` | 可空 |

### `user_session_tokens`

数据库仅保存 Refresh Token 哈希，轮换和退出会写入吊销状态。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `token_hash` | `text` | PK |
| `session_id` | `text` | NN, FK -> `user_sessions.session_id` ON DELETE CASCADE |
| `expires_at` | `timestamptz` | NN |
| `revoked_at` | `timestamptz` | 可空 |
| `created_at` | `timestamptz` | NN, DF `now()` |

### `login_rate_limits`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `key_hash` | `text` | PK；脱敏限流键 |
| `failure_count` | `integer` | NN, CK `> 0` |
| `blocked_until` | `timestamptz` | 可空 |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### `authentication_audit_events`

认证成功、失败、限流和密码变更审计。主体和来源只保存哈希化检索值。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `cursor` | `bigserial` | PK |
| `deployment_id` | `text` | NN |
| `user_id` | `text` | 可空 |
| `session_id` | `text` | 可空，FK -> `user_sessions` ON DELETE SET NULL |
| `event_type` | `text` | NN, CK `login.succeeded`/`login.failed`/`login.throttled`/`password.changed` |
| `outcome` | `text` | NN, CK `success`/`failure`/`denied` |
| `reason` | `text` | 可空 |
| `principal_hash` | `text` | NN |
| `source_hash` | `text` | NN |
| `created_at` | `timestamptz` | NN, DF `now()` |

## Skill、Credential 与 Model

### `skills` 与 `skill_versions`

| 表 | 主键 | 主要字段/约束 |
| --- | --- | --- |
| `skills` | `id` | `name` NN；`description` DF `''`；`enabled` DF `true`；`created_at`；`updated_at` |
| `skill_versions` | `(skill_id, version)` | `skill_id` FK -> `skills` ON DELETE CASCADE；`object_key`；`sha256`；`size_bytes`；`published` DF `false`；`created_at`；`published_at` 可空 |

Skill ZIP 存在对象存储中，版本表只保存对象键、SHA-256 和大小。

### `credentials`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `deployment_id` | `text` | PK 组成列，NN, FK |
| `id` | `text` | PK 组成列，NN |
| `name` | `text` | NN |
| `service` | `text` | NN |
| `type` | `text` | NN, CK `api_key` |
| `delivery_mode` | `text` | NN, CK `server_only`/`client` |
| `encrypted_value` | `bytea` | NN；应用层加密密文 |
| `nonce` | `bytea` | NN |
| `key_id` | `text` | NN；密钥环引用 |
| `masked_value` | `text` | NN；展示用脱敏值 |
| `enabled` | `boolean` | NN, DF `true` |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |
| `rotated_at` | `timestamptz` | NN, DF `now()` |

### `credential_resolution_audit`

不保存凭证明文。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `deployment_id` | `text` | NN, FK |
| `credential_id` | `text` | NN |
| `user_id` | `text` | NN, FK -> `users.id` |
| `session_id` | `text` | 可空，FK -> `user_sessions` ON DELETE SET NULL |
| `purpose` | `text` | NN |
| `outcome` | `text` | NN, CK `resolved`/`denied` |
| `reason` | `text` | 可空 |
| `created_at` | `timestamptz` | NN, DF `now()` |

### `models`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `deployment_id` | `text` | PK 组成列，NN, FK |
| `id` | `text` | PK 组成列，NN |
| `display_name` | `text` | NN |
| `source_type` | `text` | NN, CK `gateway`/`enterprise_open_source`/`local` |
| `protocol` | `text` | NN, CK `openai-compatible` |
| `endpoint` | `text` | 可空 |
| `upstream_model` | `text` | 可空 |
| `local_model_ref` | `text` | 可空 |
| `credential_id` | `text` | 可空；与 `deployment_id` 复合 FK -> `credentials` ON DELETE RESTRICT |
| `capabilities` | `text[]` | NN, DF `'{}'` |
| `reasoning_compatibility` | `jsonb` | 可空；非空时必须是 object |
| `context_window` | `integer` | 可空，CK `> 0` |
| `is_default` | `boolean` | NN, DF `false`；每个部署最多一个默认模型 |
| `enabled` | `boolean` | NN, DF `true` |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### 资源授权表

| 表 | 资源列 | 约束 |
| --- | --- | --- |
| `skill_assignments` | `skill_id` FK -> `skills.id` ON DELETE CASCADE | `id` PK；`deployment_id`；`subject_type` CK `user`/`role`/`team`；`subject_id`；`created_at`；资源与主体组合 UQ |
| `credential_assignments` | `(deployment_id, credential_id)` 复合 FK -> `credentials` ON DELETE CASCADE | 其余同上 |
| `model_assignments` | `(deployment_id, model_id)` 复合 FK -> `models` ON DELETE CASCADE | 其余同上 |

`subject_id` 是多态主体 ID。数据库限制主体类型，服务层校验主体存在、属于当前部署且已启用。

## 管控事件与状态

### `control_events`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `event_id` | `text` | PK；幂等事件标识 |
| `deployment_id` | `text` | NN, FK |
| `type` | `text` | NN |
| `scope_type` | `text` | NN, CK `global`/`team`/`user` |
| `scope_id` | `text` | 可空；`global` 时为空 |
| `resource_type` | `text` | 可空 |
| `resource_id` | `text` | 可空 |
| `resource_revision` | `text` | 可空 |
| `task_type` | `text` | NN |
| `supersedes_key` | `text` | 可空 |
| `state` | `text` | NN, DF `active` |
| `expires_at` | `timestamptz` | NN |
| `created_by` | `text` | NN, FK -> `users.id` |
| `created_at` | `timestamptz` | NN, DF `now()` |

### `session_control_deliveries`

一个事件对每个活跃用户会话生成独立投递。读取不消费，ack/result 更新投递状态。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `cursor` | `bigserial` | UQ；读取顺序 |
| `delivery_id` | `text` | PK |
| `event_id` | `text` | NN, FK -> `control_events` ON DELETE CASCADE |
| `session_id` | `text` | NN, FK -> `user_sessions` ON DELETE CASCADE |
| `state` | `text` | NN, DF `pending` |
| `attempt_count` | `integer` | NN, DF `0` |
| `received_at` | `timestamptz` | 可空 |
| `started_at` | `timestamptz` | 可空 |
| `completed_at` | `timestamptz` | 可空 |
| `applied_revision` | `text` | 可空 |
| `error_code` | `text` | 可空 |
| `message` | `text` | 可空 |
| `updated_at` | `timestamptz` | NN, DF `now()` |

UQ：`(event_id, session_id)`。

### `telemetry_events` 与 `skill_sync_results`

| 表 | 主键 | 主要字段/约束 |
| --- | --- | --- |
| `telemetry_events` | `event_id` | `deployment_id`；`user_id`；`session_id` 可空且删除会话时置空；`type`；可空资源/结果字段；`payload jsonb` DF `{}`；`occurred_at`；`received_at` |
| `skill_sync_results` | `id` | `deployment_id`；`user_id`；`session_id` 可空且删除会话时置空；`revision`；`status`；`installed_skill_ids text[]`；`payload jsonb`；`created_at` |

`telemetry_events.event_id` 提供重复上报去重语义。

## 数据平面

### `data_plane_desired_states`

每个部署一条期望路由状态。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `deployment_id` | `text` | PK, FK |
| `revision` | `text` | NN |
| `routes` | `jsonb` | NN |
| `content_hash` | `text` | NN, CK 64 位小写十六进制 SHA-256 |
| `published_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### `data_plane_statuses`

每个部署一条实际观测状态。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `deployment_id` | `text` | PK, FK |
| `state` | `text` | NN, CK `pending`/`applying`/`ready`/`degraded`/`error` |
| `observed_revision` | `text` | 可空 |
| `content_hash` | `text` | 可空；非空时为 SHA-256 |
| `last_applied_at` | `timestamptz` | 可空 |
| `error_code` | `text` | 可空 |
| `message` | `text` | 可空 |
| `resource_count` | `integer` | NN, DF `0`, CK `>= 0` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

## License

License 用于离线验证并激活客户购买的企业版客户端和服务端。数据库保存验签后的摘要、
额度与激活状态，不保存签发私钥。

### `licenses`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `license_id` | `text` | PK |
| `deployment_id` | `text` | NN, FK |
| `customer_id` | `text` | NN；供应商侧客户标识 |
| `digest` | `text` | NN, CK `sha256:<64 hex>` |
| `key_id` | `text` | NN；可信公钥 ID |
| `status` | `text` | NN, DF `active`, CK `active`/`revoked` |
| `issued_at` | `timestamptz` | NN |
| `expires_at` | `timestamptz` | NN |
| `grace_ends_at` | `timestamptz` | NN |
| `user_limit` | `integer` | NN, CK `> 0` |
| `activation_limit` | `integer` | NN, CK `> 0` |
| `features` | `text[]` | NN, DF `'{}'` |
| `payload` | `jsonb` | NN；已验签 claims |
| `revoked_at` | `timestamptz` | 可空 |
| `created_at` | `timestamptz` | NN, DF `now()` |
| `updated_at` | `timestamptz` | NN, DF `now()` |

### `license_activations`

激活绑定到部署，不绑定终端。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `license_id` | `text` | NN, FK -> `licenses` ON DELETE CASCADE |
| `deployment_id` | `text` | NN, FK |
| `user_id` | `text` | NN, FK -> `users.id` |
| `activated_at` | `timestamptz` | NN, DF `now()` |
| `last_seen_at` | `timestamptz` | NN, DF `now()` |
| `revoked_at` | `timestamptz` | 可空 |

UQ：`(license_id, deployment_id)`。

### `license_audit_events`

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `id` | `text` | PK |
| `deployment_id` | `text` | NN, FK |
| `license_id` | `text` | NN |
| `actor_user_id` | `text` | NN |
| `action` | `text` | NN, CK `import`/`revoke` |
| `outcome` | `text` | NN, CK `success`/`failure` |
| `reason` | `text` | 可空 |
| `created_at` | `timestamptz` | NN, DF `now()` |

## 迁移运行时表

### `schema_migrations`

迁移器在运行时创建，不属于业务域。

| 字段 | 类型 | 约束/说明 |
| --- | --- | --- |
| `version` | `text` | PK；迁移文件名 |
| `applied_at` | `timestamptz` | NN, DF `now()` |

## 显式索引

完成 `001` 至 `016` 顺序迁移后，当前表上保留 24 个显式索引。迁移 `013` 重命名了
部分列，但 PostgreSQL 不会自动重命名索引，所以几个历史索引名仍含 `enterprise`；
其定义已经使用 `deployment_id`，不得仅为改名而改写历史迁移。

| 索引 | 表 | 定义/用途 |
| --- | --- | --- |
| `idx_roles_deployment_enabled` | `roles` | `(deployment_id, enabled, id)`；角色列表 |
| `idx_role_permissions_permission` | `role_permissions` | `(deployment_id, permission_id, role_id)`；权限反查角色 |
| `idx_teams_deployment_enabled` | `teams` | `(deployment_id, enabled, id)`；Team 列表 |
| `idx_user_roles_user` | `user_role_bindings` | `(deployment_id, user_id, role_id)`；解析用户角色 |
| `idx_user_teams_user` | `user_team_bindings` | `(deployment_id, user_id, team_id)`；解析用户 Team |
| `idx_user_sessions_user_active` | `user_sessions` | `(deployment_id, user_id, revoked_at, last_seen_at DESC)`；活跃终端 |
| `idx_user_session_tokens_session` | `user_session_tokens` | `(session_id, revoked_at, expires_at)`；Token 轮换与清理 |
| `idx_models_default` | `models` | 唯一部分索引 `(deployment_id) WHERE is_default`；单默认模型 |
| `idx_model_assignments_subject` | `model_assignments` | `(deployment_id, subject_type, subject_id, model_id)`；授权解析 |
| `idx_credential_assignments_subject` | `credential_assignments` | `(deployment_id, subject_type, subject_id, credential_id)`；授权解析 |
| `idx_credential_resolution_audit` | `credential_resolution_audit` | `(deployment_id, credential_id, created_at DESC)`；按凭证查审计 |
| `idx_credential_resolution_audit_session` | `credential_resolution_audit` | `(deployment_id, session_id, created_at DESC)`；按终端会话查审计 |
| `idx_session_deliveries_session_state` | `session_control_deliveries` | `(session_id, state, cursor)`；按会话拉取投递 |
| `idx_session_deliveries_event` | `session_control_deliveries` | `(event_id, cursor)`；按事件查投递 |
| `idx_telemetry_session_search` | `telemetry_events` | `(deployment_id, session_id, occurred_at DESC)`；会话遥测 |
| `idx_telemetry_user_time` | `telemetry_events` | `(deployment_id, user_id, occurred_at DESC)`；用户遥测 |
| `idx_skill_sync_results_session` | `skill_sync_results` | `(deployment_id, session_id, created_at DESC)`；会话同步历史 |
| `idx_login_rate_limits_updated` | `login_rate_limits` | `(updated_at)`；清理限流状态 |
| `idx_authentication_audit_enterprise_time` | `authentication_audit_events` | `(deployment_id, created_at DESC)`；部署认证审计；名称为迁移历史兼容 |
| `idx_authentication_audit_session_time` | `authentication_audit_events` | `(deployment_id, session_id, created_at DESC)`；会话认证审计 |
| `idx_licenses_deployment_status` | `licenses` | `(deployment_id, status, expires_at)`；License 状态与过期查询 |
| `idx_license_activations_enterprise` | `license_activations` | `(deployment_id, license_id, revoked_at)`；激活状态；名称为迁移历史兼容 |
| `idx_license_audit_enterprise_time` | `license_audit_events` | `(deployment_id, created_at DESC)`；部署 License 审计；名称为迁移历史兼容 |
| `idx_license_audit_license_time` | `license_audit_events` | `(license_id, created_at DESC)`；单 License 审计 |

主键和 `UNIQUE` 约束由 PostgreSQL 自动创建唯一索引，不重复列在上表。外键不会自动
创建索引；新增索引必须以新的编号迁移提交，并用生产规模数据的
`EXPLAIN (ANALYZE, BUFFERS)` 验证。

## ORM 与事务边界

- GORM Repository 负责 User、Role、Team 以及 Skill、Credential、Model 的普通运行时 CRUD。
- GORM 与显式 SQL 共用同一个 `pgxpool`；生产代码严禁调用 `AutoMigrate`。
- Session Token 轮换、License 激活/吊销、控制事件投递、Skill 授权变更广播、默认模型切换、
  Credential 授权解析与审计继续使用显式 SQL 和事务锁。
- Schema 的唯一生产变更入口是前向、版本化 SQL migration。

## 安全边界

- 所有部署边界值从已认证会话派生，不接受客户端提升或替换。
- 多态授权的 `subject_id` 必须由服务层校验为当前部署中的 User、Role 或 Team。
- 密码、Refresh Token、Credential 明文、License 私钥和 JWT 签名 seed 均不得写入业务表或日志。
- Credential 密文由应用层密钥环保护；`masked_value` 仅用于管理界面展示。
- License 使用签发方私钥离线签名，Control Service 和企业客户端只持可信公钥并可在内网离线验签。
- AEP JWT/model JWT 公钥由 `GET /.well-known/jwks.json` 暴露；签名 seed 由部署 Secret 提供。
