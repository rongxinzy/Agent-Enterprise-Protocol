# User/Role/Team 与 User Session 切换

PR #53 将运行时身份收敛为三个概念：

- `Deployment` 是服务端单部署边界；
- `User` 是唯一业务主体；
- `Role` 和 `Team` 是授权主体；每个登录终端由独立 `User Session` 表示。

新客户端不需要生成或保存 `agentId`。登录、刷新和密码修改都会创建或轮换
`user_sessions` / `user_session_tokens`，控制事件按 `user:<deploymentId>:<userId>`
主题为每个活跃终端创建独立 cursor。一个终端 ack 不会消费同一用户其他终端的事件。

SDK 的 canonical 方法包括 `getCurrentUser`、`listModels`、
`listCredentialsForUser`、`getUserSkillManifest`、`listUserControlEvents` 和
`uploadUserEventBatch`。旧的 `listAgent*` 方法和 `X-AEP-Agent-ID` 仅保留迁移窗口，
不会被新示例客户端或 `aepctl` 发送。

管理员可以通过：

```text
go run ./cmd/aepctl --deployment demo --username admin --password ... session list
```

查看活跃终端会话。PR #54 将删除旧 Agent/Organization/Enterprise 表、字段和兼容
接口，并补充历史数据清理与升级回滚说明。
