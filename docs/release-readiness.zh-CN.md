# AEP v1 企业底座发布就绪度

状态：发布候选
实现档位：M3
企业底座完成度：90%
评估日期：2026-08-24

## 发布结论

当前企业底座可以作为独立集成包发版，并进入受控企业试点，但还不是通用可用的 GA 版本。

达到 90% 底座门禁不依赖真实产品客户端。Node 参考 Agent 已覆盖真实客户端需要使用的 SDK、会话、Skill、事件、Credential 和模型链路。使用知远账号密码档位完成 RongxinAI 主进程接入属于最后 10%。

权威机器可读评分位于 release/foundation-readiness.json。scripts/release-audit.mjs 会阻止评分漂移、证据文件缺失、工具链或协议版本不一致、不安全生产默认值以及 CI 必需门禁被删除。

## 评分

| 门禁 | 权重 | 结果 |
| --- | ---: | --- |
| 契约与 Node SDK Core | 15% | 完成 |
| 平台身份与会话生命周期 | 15% | 完成 |
| Skill 管控闭环 | 10% | 完成 |
| 管控事件与遥测 | 10% | 完成 |
| 模型控制面与 Higress 数据面 | 15% | 完成 |
| Credential 管控与 Agent 下发 | 10% | 完成 |
| 参考 Agent | 5% | 完成 |
| 生产运行基线 | 5% | 完成 |
| 产品客户端集成 | 5% | 待完成 |
| 生产数据面自动化 | 5% | 完成 |
| GA 验证与发布供应链 | 5% | 待完成 |

已完成权重严格为 90/100，待完成工作不按部分完成计分。

## 当前可发版能力

管理员可以通过 aepctl 管理账号、Skill、作用域授权、管控事件、模型、Credential、Agent、投递和审计记录。Node SDK 已提供登录、refresh 轮换、RFC 9457 错误、安全重试、全部已实现的 Agent 与管理 API、模型连接发现及 Credential no-store 校验。

参考 Agent 可持久化事件 inbox 和遥测 outbox，安全收敛 Skill 包，在重启后继续执行，通过 Higress 链路消费授权模型，并在不把明文写入 SQLite、遥测、日志或命令输出的前提下短时解析 Credential。

管控服务和 gateway-authorizer 已提供存活探针、依赖感知就绪探针、结构化日志、有界请求处理和 Prometheus 指标。生产配置会拒绝开发数据库、对象存储、签名密钥、管理员密码及 mock 联合认证配置。

## 安全与兼容门

本次发布支持的生产认证档位是管理员创建的知远账号密码。Mock OIDC 只属于开发和测试夹具。生产环境默认关闭，配置层禁止强行开启，认证方法发现不会返回它，metadata 也不会公布 federated_auth。客户身份系统接入延后，不阻塞 password-only GA 档位；未来的企业 OIDC 适配器仍须实现 Authorization Code + PKCE、一次性交换、身份映射，并保持相同 AEP 会话契约。

metadata 为支持能力发现，可以不带协议版本头访问。其他所有 /aep/v1 请求必须携带 X-AEP-Protocol-Version: 1.0。缺失或不支持的版本会收到 RFC 9457 的 426 响应及 X-AEP-Supported-Protocol-Versions 响应头。

higress-standalone 与静态 AI Proxy 映射只用于本地和 CI。生产 Higress 必须采用 Helm、受保护的供应商 Secret、TLS 入口和组织级高可用控制。

gateway reconciler 现已通过 Kubernetes server-side apply 下发租户 Ingress 与 Higress WasmPlugin。确定性名称、统一 field manager、周期漂移修复、禁用删除、部分失败状态和双副本收敛均由故障测试及真实 kind API Server 门禁覆盖。

## 剩余 10%

产品客户端集成，5%：使用知远账号密码档位完成 RongxinAI 主进程试点；密码、Token 和 Credential 明文不得进入 Renderer，refresh token 只能通过操作系统保护的存储持久化。

GA 验证，5%：完成负载与长稳测试、外部安全评审、备份与灾备演练、签名制品与 SBOM 发布，以及草案档位的剩余清理。M3 尚未开放单个 Skill 版本撤回接口；在补齐该生命周期操作前，管理员可以撤销授权、禁用或删除整个 Skill。

## Go 或 No-Go

运行完整本地发布门：

~~~sh
npm run release:check
~~~

该命令会校验 OpenAPI 与生成的 SDK 类型，构建并测试 Node workspace，校验 90% 评分，执行 Go test、race、vet 和 build，并运行全部 Compose E2E。CI 中 sdk-gate、control-service、example-agent、compose-e2e、data-plane-kubernetes 和 release-gate 必须全部成功。

出现以下任一情况时不得进入生产试点：复用任何生产默认凭证、启用 mock 联合认证、在不可信网络上缺少 HTTPS 或等效可信传输、备份责任未定义，或目标 Higress 拓扑仍基于 higress-standalone。
