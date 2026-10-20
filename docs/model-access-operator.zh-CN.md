# 模型访问原生投影

简体中文 | [English](model-access-operator.md)

这是 [#338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338) 的显式启用阶段：
在既有 Kubebuilder Operator 中添加真实控制器，消费已准入的
[ModelAccessGrant](model-access-grants.zh-CN.md) 会话绑定，生成自有的 Authorino v0.26.4
AuthConfig 与新鲜度 ConfigMap。本阶段不替换默认 authorizer，不接通共享 Higress 入口，
不实现员工工作负载鉴权，不代表完整业务验收；正式采用门禁仍未通过。

## 所有权与配置

DB 授权仍是事实源，继续使用已有事务 outbox 与 publication relay。此控制器没有自己的
DB 或 Secret 访问，不增加第二条发布链路。Grant 必须准确匹配绑定摘要名称、relay 标记、
部署标签及当前 DeploymentConfig。支持人类与 agent 的 **session** 绑定；Kubernetes
workload 绑定仍拒绝，等待独立可信身份验证阶段。

默认部署和默认 RBAC 不变。显式渲染
`kubectl kustomize services/gateway-operator/config/access-rbac` 只生成额外的
`aep-system` Role 与 RoleBinding，绑定既有 Operator 身份：读取 grant/部署配置、更新
grant status、创建/更新/监听 ConfigMap 与 AuthConfig。不增加 Secret、TokenReview、
全局绑定或源资源删除权限。已有控制器保留各自原有权限。仍须部署侧保证独占写权限；
field manager 与 owner reference 不能阻止另一个高权限写入者。

开启时必须配置：

| 变量 | 含义 |
| --- | --- |
| `AEP_GATEWAY_ACCESS_OPERATOR_ENABLED=true` | 显式启用，否则不注册新控制器 |
| `AEP_GATEWAY_ACCESS_ISSUER_URL` | 可信 AEP JWT discovery issuer |
| `AEP_GATEWAY_ACCESS_POLICY_BASE_URL` | 提供投影 Rego 文件的可信静态服务 |
| `AEP_GATEWAY_ACCESS_REFRESH_SECONDS` | 显式设置原生刷新间隔 1–60 秒；不是生产默认参数 |
| `AEP_GATEWAY_ACCESS_REQUIRE_ENTITLEMENT` | 默认 true；false 时额外接受 model JWT |

非法布尔值、时间间隔与 URL 均拒绝启动。URL 只允许 HTTP(S)，禁止 userinfo、query、
fragment。HTTP 允许项用于隔离测试，不是生产传输安全方案。策略源可以改变授权边界，
必须限制写入者与网络调用者，审查 TLS/NetworkPolicy 部署。本阶段没有交付这项生产静态
服务或 overlay；独立评审的 [HTTPS 文件源 #357](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/357)
提供此组件。Node 文件源只是一次性测试夹具，不得作为产品部署。

## 稳定语义与独立续期

每个会话绑定拥有一个稳定 AuthConfig 与一个 ConfigMap。Operator 根据实际语义及可信
部署选项独立计算 hash，不直接信任源 publication 的 contentHash。角色、团队、模型、
许可证、配置变化会改变语义；签发时间、有效期和 publication 标记不改变语义。
模型、角色、团队和许可证排序后参与计算。

原生认证缓存已验证的 AEP JWT 身份，原生 OPA 缓存完整会话决策及允许模型范围。另一个
**不缓存结果**的原生 OPA 边界每次请求校验语义 hash、实际 JSON `body.model`、Token
scope、数字时间字段、发布绝对期限、精确许可证 ID/digest 及宽限期限。热结果仍执行
这些边界检查；没有逐请求业务 DB 查询，也没有自研授权缓存。本阶段仅支持 JSON body
模型请求，不支持多模态链路中的 multipart 和从路径读取模型的请求。

续期只写 ConfigMap，不能改写 AuthConfig 的 metadata/spec/status，因为上游元数据事件
也可能重建 evaluator 缓存。先写 ConfigMap 再写 AuthConfig，混合语义版本通过 hash
错配拒绝。Sequence/owner 护栏、实时父资源检查、resourceVersion 冲突及读回限制重放与
外来资源接管。Kubernetes 没有跨对象事务：检查与子资源写入之间仍可能发生父资源变更。
Watch 重试与最后发布的绝对期限约束收敛，不能声称实现了原子撤权。

Authorino 按 evaluator/进程轮询刷新外部模块，不直接 watch ConfigMap；下载/编译失败
保留最后良好模块，其绝对期限到期后拒绝。ConfigMap volume 投影还存在独立传播延迟。
删除 grant 使用同命名空间 owner GC；GC 与策略源故障不能延长最后已编译的期限。

`Applied=True` 仅表示原生资源读回一致。`Ready` 始终 false，清空 replicas 观察记录：
API 写入、Pod 就绪、原生 AuthConfig status 与 Lease 选主均不证明所有服务进程已采用。
内部 `x-aep-access-observation` 响应只包含语义 hash/publication，不是缓存命中见证、
可信主体头或对外 API；后续入口集成必须清洗调用方身份提示，并将该证据保留在内部。

## 验证与剩余门禁

单测/API 测试覆盖所有权、旧源写入、同版本内容篡改、多余 binary data、到期、status
隔离、配置校验和可选命名空间权限。配置 `KUBEBUILDER_ASSETS` 后，CI 使用中性原生存储
schema 运行真实 API Server 测试。额外配置 `AEP_AUTHORINO_CRD_PATH` 指向固定 v0.26.4
CRD，会检查 SHA-256 并验证真实准入默认值；中性 schema 不能作为原生准入证据。

显式 probe 用 ko 构建当前 Operator，在独立临时 Kind 中运行两个 Operator 和两个固定
Authorino 副本，使用真实 AEP 签发、discovery/JWKS 输出，验证允许/拒绝、错误 JWT、实际
body 模型越权、不改 AuthConfig 的续期及禁用撤权。安装的是 Higress schema 夹具，
**不是**真实 Higress 网关、业务 DB、供应商或员工运行时。Kind 仅是本地测试工具。

```powershell
$env:KIND = 'path/to/kind'
$env:KO = 'path/to/ko'
$env:AEP_AUTHORINO_CRD_PATH = 'path/to/pinned/authconfigs.yaml'
$env:AEP_NATIVE_ACCESS_REPORT = 'path/outside/repository/report.json'
$env:AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT = 'true'
node tests/e2e/native-access-projection.mjs
```

Docker 需已缓存 `node:24-alpine`、固定 Authorino v0.26.4 tag 和脚本指定的固定 Kind
节点镜像。导入 Authorino 时保留原始 OCI index/digest；单平台导出会改写 index，可能
留下 containerd 悬空 import 引用。报告不保存凭据。

与 #357 实际双副本 HTTPS 清单联验时，额外将 `AEP_NATIVE_ACCESS_POLICY_SOURCE_DIR`
指向其 `deploy/kubernetes/access-policy-source` 目录，并设置 `OPENSSL` 可执行文件。
测试在仓库外创建临时 PKI，将临时 CA 追加到固定 Authorino 镜像的完整公共信任包。
两个授权进程消费文件源；第三个冷启动负向对照不信任该 CA，必须因 x509 校验拒绝。
进一步检查源中断、绝对到期、离线续期不能延长旧许可、恢复后采用新版本，且不改写
AuthConfig。这仍是 CR 起点的组件联验，不是业务 DB/Higress/员工或 NetworkPolicy
执行验收。临时私钥文件及自有集群均在 `finally` 清理。

### 从真实管理 API 开始的业务模式

在上述 HTTPS 参数基础上，增加以下配置后运行同一入口：

```powershell
npm run build --workspace @aep/sdk-node
$env:AEP_NATIVE_ACCESS_BUSINESS = 'true'
$env:AEP_NATIVE_ACCESS_REQUIRE_ENTITLEMENT = 'false'
node tests/e2e/native-access-projection.mjs
```

Docker 还需缓存 `postgres:17.6-alpine` 与 `rustfs/rustfs:1.0.0`。脚本用 ko 构建当前
Control Service，在自有 Kind 内部署一次性 PostgreSQL、对象存储和真实服务。随机
管理员/存储密码、签名 seed 和 Credential key 只作为运行时 Secret 注入，不进入镜像、
仓库或报告。整个夹具保留 restricted Pod Security；不访问现有本地或生产数据库。

业务写入只通过 SDK/公开管理 API：模型仅赋予角色，用户通过直接角色或团队默认角色
获取模型。验证团队默认角色、用户直接角色及团队成员变化，角色/团队停用与恢复、
角色模型授权删除与重建、单会话撤销、账号停用/重新启用、模型禁用/恢复、
真实 Control/relay 中断后的到期拒绝和恢复，以及一个鉴权进程重建后的重新发现。
只读 SQL 将最新 DB publication 的操作 ID、序号、摘要和阶段与 CR 及执行观察关联；
不通过 SQL 或手改 CR 模拟授权变更。

验收观察器独立枚举当前两个 Ready Pod，逐 Pod 直连采集 UID、containerID 和重启
次数，并在请求前后核对实例集合。此观察器只属于测试，既不是产品级全进程 observer，
也不写 `Ready=True`；内部执行响应同样不是缓存命中见证。业务模式使用真实服务签发的
人类会话 model JWT，仍不覆盖企业 entitlement、可信员工 workload、共享 Higress
入口、供应商推理、配额或 NetworkPolicy 执行。`adoptionGate` 继续为 false。

正式采用前还需完成：

- 更广业务场景的 DB→outbox→relay→CR→Operator 覆盖，包括许可证、计划到期与 agent
  绑定；产品级全服务进程发现/采用确认，以及安全共享 Higress 入口。上述人类会话
  probe 不能替代这些门禁。
- 可信员工 workload 绑定，兼容原有 Consumer/Redis 配额身份与多模态请求模型提取；
  本阶段不增加配额记账。
- 安全动态静态文件交付、watch 中断、到期恢复及部署配置变更。软件/选项升级可能在同
  source sequence 下改变渲染文件，会被护栏有意拒绝：停止旧 writer，取得新的可信
  publication，再启用改变后的渲染语义。不得混用不同配置的副本或伪改旧标记。
- 容量、并发与驱逐。原生缓存按每绑定每 evaluator 分配内存；freecache 拒绝超过其
  容量 1/1024 的单条 entry。实测企业 JWT 原始 key 加身份内容超过上游默认 1 MB 的
  单条限制。本 probe 的 4 MB 仅覆盖小型夹具，不证明最大 scopes 或部署规模可用。
  一年 TTL 只是保留上限，Token/发布/许可证期限仍独立检查；存储保留与高基数成本
  必须实测评审。

[组件续期实验](auth-result-cache-feasibility.zh-CN.md) 独立证明原生机制，不能替代上述产品
门禁。保持 `adoptionGate=false`，继续使用既有默认 authorizer。
