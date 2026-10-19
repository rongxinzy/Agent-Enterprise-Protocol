# 鉴权结果复用：组件可行性验证

简体中文 | [English](auth-result-cache-feasibility.md)

本阶段对应 [#337](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/337)，为[有效授权投影与发布 #338](https://github.com/rongxinzy/Agent-Enterprise-Protocol/issues/338)提供选型证据。它是可选的隔离实验，不替换现有 AEP authorizer，也不是部署模板。

目标结果绑定部署、主体、会话或工作负载、允许模型、策略版本和有效期。匹配请求复用结果，不重复完整身份认证和授权求值；涉及 Alice 的策略发布不能清掉 Bob 的结果。过期、会话结束与恢复后新鲜度仍有独立边界，配额消耗继续执行。

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

程序使用独立 kubeconfig，新建随机 `aep-auth-cache-*` 集群，只在该集群安装上游 CRD、两个 Authorino 副本及模拟 introspection 服务。Node 辅助镜像使用当前平台归档导入，要求 Docker 支持 `docker image save --platform`；Authorino 由集群按固定 digest 拉取，避免 Docker/containerd 的 OCI 索引导入悬空引用问题。正常成功或失败均删除自建集群与临时文件；进程被强制结束时，按 Kind/Docker 名称清理本次自建集群，不删除其他集群。不调用云端模型、不读取 AEP 密钥、不更新既有预览集群。

## 检查与判读

| 检查 | 证据和预期 |
|---|---|
| 结果复用 | 两个副本分别重复调用，introspection 次数、缓存身份序号与原生 OPA 生成的决策时间戳保持不变；只看身份序号不算完整证据 |
| 共享配置更新 | 记录共享 AuthConfig 更新同时清掉无关主体结果的限制；这是目标未满足，不能记为产品验收通过 |
| 主体配置分离 | 修改 Alice 的模型或撤销会话，两副本采用新策略；Bob 的身份和决策见证值保持不变 |
| 越权 | 跨主体复用与未知凭据拒绝，旧模型不能沿用旧许可 |
| 副本重启 | 重启副本从最新撤权配置开始冷鉴权，未重启副本保持 Bob 的缓存 |
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

## 尚未完成

逐主体配置使用合成测试域名，尚未接通产品共享网关入口的可信绑定；URL、请求头或 Pod 中的模型列表本身不能代替身份。每主体 CR 和缓存数量、容量、驱逐与更新开销需要独立验证。

Authorino 仍接收每次请求的外部鉴权子请求；缓存命中避免服务内的完整认证与策略求值，不等于网关内零子请求。没有据此宣称性能收益。

缓存按进程独立：首次命中每个副本都会完整鉴权，冷缓存或缓存驱逐后也会重新执行。因此尚未证明“整个部署只在首次及相关策略变化时完整鉴权”的严格保证。正式采用前需解决共享缓存支持或冷缓存语义；探针没有另写分布式缓存来掩盖该限制。

后续仍需验证业务 DB 的角色/团队变化，经既有 outbox、publication relay、CR 和 Operator 到所有实际服务副本的可靠发布，以及单调版本、旧版本重放、传播失败新鲜度、Consumer/Redis key、许可证与账号变化、模型请求体、可信头清理及员工工作负载绑定。本次模拟不替代现有 JWT 的签名、issuer/audience、时间与 scope 安全检查，亦不代表真实员工模型链路验收。
