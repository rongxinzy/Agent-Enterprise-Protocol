# 外部安全评审手册

AEP 企业底座从发布候选提升为 GA 前必须完成独立外部安全评审。持续运行的依赖
扫描和 CodeQL 是评审输入，不能替代独立评审。

## 评审对象

从 `main` 冻结一个提交并记录完整 40 位 SHA。为该提交生成 foundation Release，
向评审方提供源码、离线 Bundle manifest、源码/镜像 SBOM、部署清单、OpenAPI
Bundle 和公开测试证据。客户凭据、License 私钥、签名 seed、生产数据和签名器
日志绝不能作为评审输入。

最低组件范围包括：

- Node SDK 传输、会话恢复、Token 轮换和错误处理；
- control-service 的认证、RBAC、授权、事件、遥测、Credential 存储/下发、
  License 门控和管理 API；
- gateway-authorizer 的 JWT/JWKS 校验、模型作用域、授权状态、请求上限、流式
  转发和上游故障处理；
- gateway-reconciler 的 Kubernetes 权限、期望状态校验、Secret 引用和部署隔离；
- 参考 Agent 的 inbox/outbox、Skill ZIP 解压、checksum、Credential no-store 和
  用户 topic 事件投递；
- Compose/Kubernetes 默认值、网络边界、探针、日志、备份恢复、离线安装和发布
  供应链。

闭源 Zhiyuan 企业扩展和 Admin Console 必须在其自身仓库单独评审；除非报告明确
包含准确源码与提交，否则不得把它们的结论写成 AEP 评审结论。

## 必测攻击类型

至少覆盖 OWASP ASVS/API Security Top 10、认证绕过、会话重放、refresh token
并发、跨 user/role/team 的 IDOR、提权、JWT 混淆与过期 JWKS、模型/供应商配置
SSRF、请求走私与流式资源耗尽、ZIP 路径或链接逃逸、Credential 外泄、审计/日志
泄密、SQL/对象标识注入、事件 topic 泄漏、重放/幂等失败、License 篡改、不安全
生产默认值以及依赖和容器漏洞。

## 证据与退出标准

评审报告必须给出评审方、日期、方法、准确提交、组件范围、环境、发现项、严重度
方法、修复和复测结果。机密报告存放在批准的证据系统；仓库只允许保存不敏感的
结论证明与报告 SHA-256。

GA 放行必须同时满足：

1. 未关闭的 critical/high 发现为零。
2. 每个 medium 发现都有负责人、缓解措施和截止日期。
3. 已修复的 critical/high 发现经过独立复测。
4. 在被评审提交上通过 `npm run release:check`、安全工作流、离线安装、备份恢复
   和产品集成门禁。
5. 发布 manifest 指向同一提交，全部制品通过 `SHA256SUMS` 校验。
6. 指定发布负责人记录 go/no-go；在证据存在之前，状态必须保持
   `release-candidate` 和 95%。
