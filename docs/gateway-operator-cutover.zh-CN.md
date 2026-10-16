# 网关 Operator 切换与联合恢复

适用范围：AEP 网关配置面。这里只提供受审流程和 opt-in 目标清单，不代表已在生产切换。
数字员工生命周期、compute 分配和业务默认值继续由原有业务/运行时负责；本阶段不新增对应 CRD。

## 交付清单

- `kubectl kustomize deploy/kubernetes/production`：保留旧部署兼容默认值。
- `kubectl kustomize deploy/kubernetes/operator`：新链路目标状态。旧 Deployment 为零副本，
  三个旧 RoleBinding 的 subjects 显式清空，避免仅从输出中移除后仍残留线上权限。
- `kubectl kustomize deploy/kubernetes/operator-observation`：另行批准的 Envoy 观测权限。
  port-forward 可访问网关 Pod 任意端口，不是只读 HTTP 权限；不需要 exec 或公开 admin 端口。
  未启用时 Ready 保持 Unknown，不能将 Applied 当作数据面成功。

CRD 与 runtime 分开安装；共享 runtime 不重复声明 Namespace，也不使用关闭 Kustomize
load restrictions 的方式引用文件。安装 CRD 后等待 Established，再安装自定义资源。
交付 overlay 必须固定 control、Operator、publisher 镜像 digest，并配置一致的 deployment ID、
Redis 地址、TLS、数据库、Secret store 和 CNI 实际 API 地址/端口；示例不是可直接投产的客户配置。
发布组件数据库 URL 必须使用独立只读账号（credentials/models SELECT），keyring 与控制服务一致。
基础设施密钥由 ESO 提供；供应商 Secret 仅由选定的交付者管理，ESO 和 DB publisher 不能同时写同名 Secret。
已有 ExternalSecret 不因从渲染结果中省略而自动删除，必须单独评审停供和 Owner 级联删除窗口。

## 切换顺序

1. 备份并记录同一恢复点的 PostgreSQL、keyring、签名材料、Secret 系统、CR spec/UID、
   已选 publication tuple 和原生 Redis 配额/限流状态。Higress 插件包含密钥，禁止将完整对象、
   Secret 或 config_dump 写入 PR、公开日志或普通临时文件。用受保护备份系统保存。
2. 冻结管理写入和推理入口，排干在途请求。停止全部 control-service 副本与旧 reconciler，
   等待旧 Pod 消失，再撤销旧 SA 的所有资源写权限（包括客户自定义/集群级绑定）。
   用 `kubectl auth can-i --as=system:serviceaccount:aep-system:aep-gateway-reconciler`
   检查 Ingress/WasmPlugin/EnvoyFilter 写入均为 no；只设置副本数或环境变量不够。
3. 安装 CRD 并等待 Established。提供正确的 `DeploymentConfig/deployment`、Secret 交付和原生设置。
   对原来同名 Higress 对象核对实际 deployment、资源内容、归属和清单，只清理经审批确认属于
   退役 writer 的对象；使用 UID/resourceVersion 删除前置条件。外来对象冲突必须停止，
   不使用 force-conflicts、伪造 owner UID 或删除任意 finalizer 来接管。
4. 在仍冻结入口的窗口应用经评审的 Operator overlay。控制服务 Recreate 禁止新旧模式混跑；
   全部副本显式 `AEP_GATEWAY_OPERATOR_MODE=true`。凭据 publisher、relay 和 Operator
   分别只有 Secret 交付、AEP CR 投射、Higress 写入权限；不向 control-service 授予 Secret 写入。
5. 若已有已选 publication，用下面的 replay；首次迁移没有已选快照时，只能评审目录和规则后
   使用 queue 捕获新的批准意图，不能假定旧 desired 表和未发布草稿等价。
6. 核对同一 operation/revision/hash 的 Accepted、Applied、全副本新鲜 Ready。验证实际
   200、跨模型共享 429、配额消耗、凭据注入、删除规则后恢复；成功后才开放入口。
   暂停 relay 时仍保留显式 Operator mode 和只读观测权限，旧入口必须继续返回 503。

## 管理 CLI

通过受保护环境提供 `AEPCTL_BASE_URL`、`AEPCTL_DEPLOYMENT`、`AEPCTL_USERNAME`、
`AEPCTL_PASSWORD`，不要把密码放命令行。幂等键不是秘密，但每次新意图使用新键，重试保留原键。

```bash
aepctl data-plane publication queue --key release-reviewed --include-gateway-limits
aepctl data-plane publication get --operation-id <operation-uuid>
aepctl data-plane publication replay --operation-id <latest-operation-uuid> --key recovery-reviewed
```

queue 捕获当前目录；显式 include 才捕获规则草稿。replay 只复制最新已选不可变路由/规则，
新建 operation/sequence；不读草稿。相同键优先返回原操作，旧来源或待撤下引用返回 409。
202/投递完成不是 Applied 或 Ready；查询条件需绑定当前 revision，旧证据不能复用。

## 故障与联合恢复

| 场景 | 行为与处理 |
| --- | --- |
| relay 丢响应/重启 | 同键返回原操作；outbox 重试和资源版本护栏收敛，不手动新建重复意图。 |
| Operator 重启/渲染对象丢失 | watch/有界重同步修复；UID 归属冲突拒绝，证据过期不能继续显示 Ready。 |
| CR 落后 DB | replay 最新快照得到新 sequence，等待 CR、资源和独立观测一致。 |
| DB 恢复到 T，CR 仍为 T+1 | 高 sequence 必须阻断较旧投射；不得提高 DB sequence、清标记或强推旧配置来绕过。 |
| pending 引用撤下 | 先恢复自动撤下投递；不能重放失效主体/模型的旧配置。 |

DB/CR 反向漂移必须冻结所有入口、relay、Operator 和凭据交付组件，保存当前证据并选择**同一批准恢复点**。
优先恢复与 DB 相匹配的受保护 CR/Secret/Redis 备份；若确认丢弃 T+1，则维护者核对 T+1
仅属于该部署的 CR 和全部下游资源，按 UID/resourceVersion 前置条件清理，经审批处理阻挡清理的
finalizer（仅在对应清理职责已人工完成后），再预置无发布标记的 DeploymentConfig。
启动 control（mode=true、relay 暂停），replay 最新已选快照，然后恢复 relay 和 Operator。
不要在其他 writer 仍运行时清理/降级 CR，也不自动清理外来对象。恢复所有证据和 200/429 后再开放入口。
快照没有的额外 CR/策略同样要核查，不能只重建被删除的一条路由。

## 回滚

回滚也需要冻结入口并排干新 writer，停用 relay、Operator 与 publisher，再撤销其写权限。
先核对旧镜像与前向 schema 兼容；从最新批准不可变快照通过受审流程准备旧 desired/原生规则，
不能回退到可能落后的 legacy 表，也不能重新捕获草稿。清理经确认属于新 writer 的下游对象，
恢复唯一旧 writer 的 RBAC/配置后才启动；验证真实调用和限额。无法证明语义一致时保持关闭，
按联合恢复点恢复，禁止只关 mode 开旧 reconciler。

运行时兼容代码保留给未迁移部署。生产全部迁移和回滚窗口关闭后，才能单独删除旧 runtime/入口；
本阶段保留兼容不表示新部署仍运行旧 writer。
