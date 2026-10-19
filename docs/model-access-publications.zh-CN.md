# 按主体绑定的模型授权发布账本

这是[有效授权 #338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338)
的持久投递阶段，为**现有 publication relay**提供存储及 repository 操作。
本阶段未启动消费者、未新增 Kubernetes writer、未切换默认鉴权，也没有新增 HTTP/SDK
接口。业务授权推导与数据面生效确认仍需后续集成。

## 发布流与不可变输入

迁移 `053_model_access_publications.sql` 新增：

- `model_access_streams`：保留主体/绑定身份，以及已见业务版本和该绑定的发布序号。
- `model_access_publications`：不可变 JSON 字节、摘要、版本、有效期、操作标识；可更新
  的仅有投递阶段和错误状态。
- `model_access_publication_outbox`：投递租约、重试时间、次数与完成记录。

每条流对应一个主体的会话或可信工作负载绑定。内部生产者必须以完整身份生成
`binding_key`，存储拒绝将它改绑其他主体。每个绑定独立递增发布序号；Alice 的新配置
仅取代自己的待投递记录，不取代 Bob。新配置即使仍在排队，旧配置的成功记录也不能
算作当前生效依据。

`QueueModelAccessPublication` 接收**已推导、不含凭据**的内部 `ModelAccessIntent`。
角色授权、可信身份、投影 schema 校验属于生产者职责，不能把此方法作为接收任意
客户端 JSON 的通用 API。载荷不得包含私钥、云端 key、令牌或已解密 Credential。
存储校验对象 JSON、1 MiB 上限、原始字节 SHA-256、身份长度及不超过一小时的正有效期，
并根据数据库时间拒绝过期或尚未签发的输入。

载荷使用 `bytea` 保留原始字节，避免 PostgreSQL JSON 格式化改变摘要。数据库触发器
拒绝修改快照输入，拒绝回退流版本/序号或改绑主体。部署存在时不得删除流护栏；用户、
会话或 CR 删除不能重置历史。部署删除会级联清理，本阶段不启用自动 GC。

`source_version` 必须来自持久业务工作版本，不能使用时间戳或客户端自报值。旧输入
被拒绝；相同版本不能修改内容或有效期。更高业务版本若载荷摘要和截止时间完全不变，
保留原操作和发布序号，即使只改了显示用 revision。流仍推进业务版本护栏，防止旧工作
重放。生产者必须先将模型/角色集合规范化；存储不会自行把不同 JSON 字节判为相同授权。

截止时间变化属于新投递工作。账本**没有实现安全续期，也不证明续期不会清空原生鉴权
缓存**。相同业务版本的终态失败保持稳定；新的业务工作版本可以创建新发布来重试原策略。

## 事务与 worker 集成

通过 `WithTransaction` 提供的 `DeploymentStore` 调用，快照、发布与 outbox 加入同一
事务。后续生产者需要复用配置锁和预期版本护栏，将读取有效业务状态、持久快照/outbox、
确认变更信号放在一个事务内；事务内不允许网络或 Kubernetes 操作。
本阶段尚未接入该消费者，独立的变更输入见 [#345](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/345)。

认领当前流使用 PostgreSQL `FOR UPDATE SKIP LOCKED`，忙碌绑定不阻塞其他绑定。
投递租约 30 秒；崩溃后可持新 token 重新认领相同输入。许可快照过期后明确记录
`ACCESS_SNAPSHOT_EXPIRED`，不重投旧允许配置。

完成确认要求同时满足：仍是该绑定最新操作、认领 token 正确、租约及快照有效。
空阶段表示延迟重试，`projected` 只代表 CR 投递，**不代表所有服务进程已采用**。
终态错误仅保存受限结构化代码，不保存原始 SQL/上游错误。后续 CR 适配器仍需校验
schema/hash，并以所有权、resourceVersion 和序号拒绝迟到的 Kubernetes 写入。

## 依赖与待完成门槛

本迁移不引用 052 的信号表，也不依赖 [#344](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/344)
的 `ModelAccessGrant` Go 类型，文件均不重叠，可独立合并。后续集成需要这些阶段，并扩展
现有 relay，不能另起并行 writer。账本自身不推导业务授权、不撤销运行时访问，不包含安全
续期、全副本确认、历史 user/team 直接授权收敛或人类/员工完整请求验收；门槛通过前保留
现有安全路径。

## 验证

明确设置 `AEP_REFERENCE_TEST_DATABASE_URL` 指向允许创建数据库的可丢弃 PostgreSQL：

```sh
go test -race ./services/control-service/internal/repository ./services/control-service/internal/db
```

每个测试创建随机数据库并仅清理自己的数据库。覆盖原子回滚、字节/摘要不变、不可变输入、
独立序号与相同结果复用、旧业务版本重放、跨部署与改绑、旧投递取代、租约恢复、退避及
终态修复、并发幂等、忙碌绑定不阻塞其他用户、许可到期与非法输入。未设置环境变量时，
这些 PostgreSQL 测试会跳过。
