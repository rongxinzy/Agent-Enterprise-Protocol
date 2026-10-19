# 鉴权结果复用：组件可行性验证

简体中文 | [English](auth-result-cache-feasibility.md)

本阶段对应 [#337](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/337)，为[有效授权投影与发布 #338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338)提供选型证据。它是可选的隔离实验，不替换现有 AEP authorizer，也不是部署模板。

目标结果绑定部署、主体、会话或工作负载、允许模型、策略版本和有效期。有有效热缓存时，匹配请求复用结果，不重复完整身份认证和授权求值；首次命中另一副本、副本重启或缓存驱逐后的冷缓存允许重新完整求值，不要求整个部署只鉴权一次。涉及 Alice 的策略发布不能清掉 Bob 的结果。过期、会话结束与恢复后新鲜度仍有独立边界，配额消耗继续执行。

## 本阶段范围

固定版本检查了 Higress v2.2.4 `ext-auth`、Envoy v1.36.4 `jwt_authn` / `ext_authz` 的配置，以及 Authorino v0.26.4 的身份、授权缓存和控制器。前两者的所查配置没有提供完整结果缓存与按主体失效接口；JWT 验签缓存与路由缓存不是本项能力。这不是对所有成熟组件的穷尽结论。固定源码链接与摘要见[英文技术记录](auth-result-cache-feasibility.md#components-examined)。

Authorino 提供现成的求值器结果缓存、OPA 策略执行和 Kubernetes 配置监听，是待验证候选。没有在产品中新增自研缓存或策略解释器，也没有据此决定采用 Authorino。

## 运行

在仓库根目录，准备 Node 24、Docker、kubectl、Kind、镜像仓库与 raw GitHub 网络，以及独立单节点测试集群的容量后执行：

```bash
node tests/e2e/auth-result-cache.mjs
```

可设置 `KIND` 指向 Kind 程序，`AEP_AUTH_CACHE_NODE_IMAGE` 指定兼容节点镜像，`AEP_AUTH_CACHE_REPORT` 保存 JSON 报告。raw GitHub 不可用时，用 `AEP_AUTH_CACHE_CRD_PATH` 指向固定上游 CRD 的本地副本；程序仍校验 LF 归一化后的 SHA-256，不放宽完整性检查。

设置 `AEP_AUTH_CACHE_GATEWAY=1` 可增加下述同一入口的原生 Envoy 外部鉴权验证；测试集群需要能从 Docker Hub 拉取镜像。Kind 仅用于本地临时测试，不是新增产品组件或生产依赖；生产 Kubernetes/k3s 的部署不依赖本脚本。

程序使用独立 kubeconfig，新建随机 `aep-auth-cache-*` 集群，只在该集群安装上游 CRD、两个 Authorino 副本及模拟 introspection 服务。副本启用原生 `--enable-leader-election`，以命名空间内 Lease 选出状态写入者，两个副本仍独立监听配置并服务。Node 辅助镜像使用当前平台归档导入，要求 Docker 支持 `docker image save --platform`；Authorino 由集群按固定 digest 拉取，避免 Docker/containerd 的 OCI 索引导入悬空引用问题。正常成功或失败均删除自建集群与临时文件；进程被强制结束时，按 Kind/Docker 名称清理本次自建集群，不删除其他集群。不调用云端模型、不读取 AEP 密钥、不更新既有预览集群。

## 检查与判读

| 检查 | 证据和预期 |
|---|---|
| 结果复用 | 两个副本分别重复调用，introspection 次数、缓存身份序号与原生 OPA 生成的决策时间戳保持不变；只看身份序号不算完整证据 |
| 共享配置更新 | 记录共享 AuthConfig 更新同时清掉无关主体结果的限制；这是目标未满足，不能记为产品验收通过 |
| 主体配置分离 | 修改 Alice 的模型或撤销会话，两副本采用新策略；Bob 的身份和决策见证值保持不变 |
| 越权 | 跨主体复用与未知凭据拒绝，旧模型不能沿用旧许可 |
| 副本重启 | 分别重启非状态 leader 与状态 leader，以 Lease 和 Ready Pod 核对角色，leader 重启后等待新 holder；每次恢复都拒绝已撤销访问，Bob 冷鉴权一次后连续三次复用完整结果，未重启副本的缓存不变 |
| 到期 | 单靠缓存 TTL 复现过期结果仍被使用；增加不缓存的原生到期条件后，缓存命中仍拒绝过期结果，且不增加 introspection 调用 |

报告固定 `adoptionGate: false`。`completed: true` 仅表示完成上述组件行为验证，包括成功复现已知限制；不表示产品架构通过。`cleaned: true` 表示 Kind 列表已不含本次集群。

## 2026-10-19 实测结果

隔离 Kind 集群中的两个服务副本完成了全部八项观察。重复请求的身份序号与授权时间戳保持不变；预热两个独立副本时，每个主体总计两次 introspection。逐主体配置更新、会话撤销、跨主体拒绝和副本重启均符合断言。报告为 `completed: true`、`adoptionGate: false`、`cleaned: true`。

同时复现两项限制：共享 CR 更新会清掉无关主体的缓存；未设置独立有效期条件时，缓存的 introspection 结果会超过令牌有效期。原生、不缓存的到期条件能拒绝过期结果，且不新增 introspection。AuthConfig 初始化状态写入也会触发缓存重建，因此探针等待 Ready 和初始化稳定后才测量持续命中；Ready 本身不算所有数据面副本确认，策略发布后仍逐个副本验证。

结论仅支持继续研究接入，不代表已选型。产品共享入口的逐主体可信绑定、贯穿发布链路的所有服务副本确认，以及监听中断时的旧权限有效上限仍未验证。

## 同一入口验证扩展

可选扩展固定 Envoy v1.36.4 镜像 digest `sha256:08acc53db6f4ac46e77e36b203c5254b4a2549869314bb54cf97aab81baa4566`。两个代理 Pod 使用相同的 `gateway.probe.invalid/v1/chat/completions` 入口，各绑定一个 Authorino 副本，保证逐一检查两个服务路径；这是确定性的测试配置，不是生产高可用拓扑，也不是 Higress 插件兼容性验收。

原生路由与 `ext_authz` 上下文扩展根据不可信的主体提示选择 AuthConfig。提示本身不能授予权限：配置必须核对解析出的真实主体与部署，缓存按完整 bearer 凭据区分。测试尝试用 Alice 的凭据选择 Bob，以及伪造身份头；实际 AEP JWT 验证、员工身份与产品 API/SDK 接入仍未覆盖。

缓存结果包含完整允许模型范围，与提示词无关。后续原生 OPA 条件只检查实际 JSON 请求体的模型是否处于该范围与凭据 scope 内，以及凭据到期和固定的发布有效期上限；这是逐请求范围/有效期检查，不重新展开完整主体与角色授权。探针还检查伪造允许模型头、变化提示词保持缓存、只更新 Alice 时 Bob 不变，以及停止续期后拒绝旧许可。有效期上限只验证旧许可可被拒绝，尚未实现 AEP 发布链路的故障发现和续期。测试中的简单 JSON 解析也不代表产品已覆盖重复键、流式或超大请求。

2026-10-19 已实际运行该扩展：新增四项检查均在两个服务路径通过。连同原有八项，报告共十二项观察，`completed: true`、`adoptionGate: false`、`cleaned: true`。未知选择器返回 Authorino 原生的 404 拒绝，没有放行到上游。结果确认了测试配置中的请求关联与范围边界，尚未解决下文的进程独立缓存及产品接入门槛。

## 共享状态补充核查

本节是源码核查，没有新增运行时验收，前述十二项报告不变。Higress v2.2.4 对应固定提交 `58666ac985cee19a0a9a353421c63cead6d0cb47`；完整固定源码入口见[英文记录](auth-result-cache-feasibility.md#shared-state-follow-up-source-evidence)。

Authorino 的固定版本缓存构造器直接创建进程内 Freecache，启动参数只提供容量设置，没有可选的 Redis 后端。依赖中存在通用缓存库，不等于产品已支持配置共享缓存。

Higress `response-cache` 虽支持 Redis，但不能直接放到 Authorino 前面充当完整结果缓存：

- 它缓存响应体值，命中后返回固定 200、内容类型和缓存标记，不还原原始鉴权状态与身份响应头。空响应体不会写入缓存；Authorino 原生 HTTP 成功路径恰好通过响应头交付结果，响应体为空。现有探针的 gRPC ext_authz 也不能由这个 HTTP 响应缓存直接包裹。
- 所查未命中路径与 Redis provider 使用读取后普通 SET/SETEX，没有提供分布式首次请求合并或按主体/版本失效接口。“支持 Redis”本身不能证明跨副本只鉴权一次。
- 命中和未命中的 Info 日志包含缓存 key。直接把 bearer 凭据用作 key 会泄漏到日志，不能照搬 Authorino 的隔离测试配置。安全的不透明 key 本身也不能补齐响应、失效与并发语义。

另核查了 C++ `key-auth` 的凭据到 Consumer 哈希查找、路由 allow-set。它们可作为轻量请求关联和扁平范围检查，无须展开角色或查询业务 DB，可继续研究如何消费预先计算的授权。所查配置与请求路径本身没有会话到期或发布有效期检查；删除凭据可在配置传播后撤权，但发布中断不会自行让旧配置到期。路由 allow-set 也不能证明请求体 model 与匹配路由一致。会话独立绑定、安全凭据交付、可信头、稳定配额身份及所有服务副本确认仍待实现和验证。本阶段没有运行 key-auth 验收。

因此，Redis 响应缓存或单独 Consumer allow-set 都没有补齐当前采用门槛。保留现有 authorizer 路径，不另写 AEP 缓存/执行器来让实验过关，也不把投影 API key 当作员工无应用令牌身份。选定机制前仍须证明完整结果、安全 key、到期、主体/版本失效、并发首次调用及多副本恢复。上述结论仅限所查组件和组合，不代表排除了所有其他成熟方案。

## 已确认的冷缓存边界

2026-10-19 明确允许按副本独立缓存：首次命中另一副本、副本重启或缓存驱逐后可以重新完整鉴权。有有效热缓存时仍须同时复用身份和授权结果，无关主体的策略发布不能清掉该结果。不再把整个部署只鉴权一次作为采用前提；该确认不等于允许组件上线，也不放宽撤权、到期、可信身份或发布新鲜度。

报告增加 `cacheSemantics.scope: replica`、`coldMiss: full-evaluation` 与 `warmHit: identity-and-authorization-reuse`。非状态 leader 与状态 leader 的重启断言分别检查 Bob 新增一次 introspection 后连续三次完整结果复用、未重启副本的见证值不变，以及 Alice 已撤销会话被拒绝。驱逐属于允许的冷缓存边界，本探针未另行制造驱逐并验收。此例外不允许任意定期清空或无关策略更新导致的扩大失效。

后续一次未启用状态选主的运行复现：另一副本重启时，未重启副本中 Bob 的身份和授权见证值也发生变化。保留了该断言；固定版本上游采用独立配置调和与状态管理器，原生 `--enable-leader-election` 只对状态写入者选主，两个副本的配置监听继续工作。测试清单启用此选项并授予命名空间内 Lease 权限。选主与 CR Ready 仍不等于所有副本生效，数据面见证值继续逐个路径检查。

2026-10-19 完整重跑完成十三项观察，包含两项已知目标限制的复现，以及两种角色的重启。每次重启均记录 Bob 一次冷鉴权后连续三次复用，另一副本的身份和授权见证值不变，Alice 已撤销访问继续拒绝；只有状态 leader 重启时原生 Lease holder 发生切换。同入口扩展也完成。报告为 `completed: true`、`adoptionGate: false`、`cleaned: true`，既有集群与默认流量不变。期间 Envoy 镜像仓拉取短暂失败，由 kubelet 在既有测试时限内自动重试恢复，未更换固定镜像或放宽断言。

## 尚未完成

逐主体配置使用合成测试域名，尚未接通产品共享网关入口的可信绑定；URL、请求头或 Pod 中的模型列表本身不能代替身份。每主体 CR 和缓存数量、容量、驱逐与更新开销需要独立验证。

Authorino 仍接收每次请求的外部鉴权子请求；缓存命中避免服务内的完整认证与策略求值，不等于网关内零子请求。没有据此宣称性能收益。

缓存按进程独立：首次命中每个副本都会完整鉴权，冷缓存或缓存驱逐后也会重新执行。该行为已纳入上述确认的冷缓存边界，无须仅为消除这些冷调用而新增分布式缓存。有有效结果时的复用与定向失效仍是要求，缓存期限、容量及并发冷请求仍需测量；其他接入门槛未完成，`adoptionGate` 保持 false。

后续仍需验证业务 DB 的角色/团队变化，经既有 outbox、publication relay、CR 和 Operator 到所有实际服务副本的可靠发布，以及单调版本、旧版本重放、传播失败新鲜度、Consumer/Redis key、许可证与账号变化、模型请求体、可信头清理及员工工作负载绑定。本次模拟不替代现有 JWT 的签名、issuer/audience、时间与 scope 安全检查，亦不代表真实员工模型链路验收。
