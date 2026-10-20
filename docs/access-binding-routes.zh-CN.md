# 会话选择路由的发布与回收（实验阶段）

本阶段将此前验收脚本手工创建的会话选择路由，改为由 Kubebuilder Operator
维护。业务事实源仍是 PostgreSQL，沿用 outbox/relay 发布的 `ModelAccessGrant`。
没有新增逐请求 DB 查询、令牌验证器或结果缓存实现。

**此入口仅供隔离验收，不加入默认或生产 overlay，也不切换现有 authorizer。**
保留头完整清洗、可信 Consumer/Redis 配额身份、员工工作负载身份、完整实例采用
证据与容量验收仍须分别通过。不能用本阶段的 `Applied` 代替这些采用门槛。

## 请求如何交接

```text
会话 JWT + 不可信绑定提示 + 正文模型
  → Higress 的会话选择路由
  → 原生 gRPC ext_authz → Authorino 对应 AuthConfig
  → 验证实际主体/会话/令牌边界、当前授权与正文模型
  → 覆盖绑定提示和已验证模型头，清除路由缓存
  → 现有 ModelRoute → 原有 ai-proxy/供应商协议/服务端凭据 → 模型
```

选择路由不复制供应商配置，其目标是故意不存在的 `aep-access-unrouted`。
授权成功后，Authorino 将 `x-aep-access-binding` 覆盖为 `authorized`，将
`x-aep-model-id` 覆盖为经过检查的实际正文模型；原生 `clear_route_cache`
触发重新选择，进入原有模型路由。未成功交接时不能直接发送到供应商。
外部传入 `authorized`、模型头或其他用户的绑定提示均不是授权凭证。

原生 header-mutation 紧随 ext_authz 清除调用方 bearer、绑定提示及内部观察头，
然后才交给 ai-proxy 注入供应商凭据。不能在最终路由转发阶段统一删除
`authorization`，那也会误删 ai-proxy 已注入的合法供应商密钥。
这只清理上述精确名称，不宣称已经清理所有动态 `x-aep-*` 保留头或接通配额身份。

本阶段按实际验证的 Higress 2.2.4 RDS 名称 `higress-rds-80.*` 选择 HTTP 80
配置；`*` 是该资源名称的一部分，不是自行推断的通配规则。
不能用 Istio 的 `routeConfiguration.portNumber` API 写入成功代替实际 RDS
采用检查。升级 Higress 时须重新确认这一配置名称及 filter 顺序。
本阶段只覆盖专用 Higress HTTP 80 listener 上的 `/v1/` 请求。路由还匹配
部署配置的公共 Host（接受可选的 `:80`）；原有模型 Ingress 的 wildcard
virtual host 不影响此约束。TLS listener、Anthropic 路径、多模态大请求等
不能据此视为已验收。请求体上限为 8 KiB，超过上限拒绝，不截断后放行。
全局 ext_authz 在其他业务/WASM filter 之前执行，失败关闭；专用 listener
不得混入需要豁免授权的其他业务。

绑定提示目前仍是实验选择机制，不是已发布的客户端 AEP 契约。每次请求仍经过
原生 ext_authz；Authorino 复用有效鉴权结果，并独立检查新请求的模型与有效期。
副本首次接流量、重启或缓存驱逐后的冷缓存例外沿用既有设计。

## Operator 生命周期

配置 `AEP_GATEWAY_ACCESS_INGRESS_HOST` 后，每个可用会话对应一个位于
`higress-system` 的同名 EnvoyFilter。续期不改写稳定选择路由；目标删除或
spec 漂移会触发重新调和。撤销、空模型集和 publication 到期均回收路由。

源 CR 位于 `aep-system`，不能用跨 namespace ownerReference 做垃圾回收。
Operator 在源 CR 上安装 `gateway.aep.zhiyuan.io/access-route` finalizer，
目标使用源 UID 标签和绑定注解记录归属。删除带 UID/resourceVersion 前提，
确认目标真正 NotFound 后才移除源 finalizer。旧 publication 不能清理已续期
来源的路由。外部同名对象不接管、不删除；归属冲突或目标自己的 finalizer
会保留源清理护栏并重试。不要通过强删 finalizer 绕过未完成的清理。

原有 AuthConfig/ConfigMap 继续使用同 namespace ownerReference。路由 API
写入和读回只表示配置存储成功，`Ready` 仍为 false，不代表全部 Envoy 或
Authorino 进程已采用，更不代表配额与完整请求链路已验收。

## 隔离环境配置

先准备已固定版本的 Higress、Authorino、原有模型发布链路及 HTTPS policy
source。为服务发现选中的所有 Authorino Pod 添加
`gateway.aep.zhiyuan.io/access-verifier: "true"` 标签，并确认其提供原生
gRPC 50051、可读取 AuthConfig、信任 issuer 与 policy source CA。
该 Service 的 selector 不能只覆盖部分实际服务副本。

除既有 access operator 参数外，显式启用独立权限和入口：

```sh
kubectl apply -k services/gateway-operator/config/access-rbac
kubectl apply -k services/gateway-operator/config/access-ingress-rbac
kubectl apply -k deploy/kubernetes/access-ingress
kubectl -n aep-system set env deployment/aep-gateway-operator \
  AEP_GATEWAY_ACCESS_INGRESS_HOST=models.acceptance.example
```

参考 [Operator access 参数](../services/gateway-operator/cmd/access.go) 和
[原生入口清单](../deploy/kubernetes/access-ingress/filter.yaml)。权限均通过
namespace Role 绑定，新增权限不含 Secret 或业务资源删除权。
`modelaccessgrants` 的 patch 仅用于源 finalizer；EnvoyFilter 写权限独立启用，
不扩大其他 informer 的 namespace 范围。部署方仍必须保证网关资源单写者。

首次开启或修改渲染参数需产生新的 publication；不能让旧 sequence 的配置
被新部署参数默默重写。该示例域名不是实际可用部署输入，必须替换并核对入口。

## 验证与回退

相关回归测试：

```sh
go test ./services/gateway-operator/... ./tests/deployment
# 使用仓库固定版本的 envtest 二进制和 Authorino CRD，执行真实 admission：
KUBEBUILDER_ASSETS=/path/to/envtest \
AEP_AUTHORINO_CRD_PATH=/path/to/authorino.kuadrant.io_authconfigs.yaml \
go test -race ./services/gateway-operator/...
```

真实网关还必须验证：Operator 自动创建/恢复/撤销选择路由，成功进入原有模型
路由并保留协议/模型映射，冒用绑定和正文模型提权无上游请求，停止授权副本时
失败关闭。此前直接 echo 的通路不能证明现有 ai-proxy 交接有效。

2026-10-20 的本地隔离组合验收通过 20 项检查：实际业务 API → PostgreSQL →
outbox/relay → ModelAccessGrant → Operator 选择路由 → Higress 2.2.4 →
原有 ModelRoute/ai-proxy → Mock 模型。使用随机临时供应商密钥验证原有凭据
引用及注入仍有效，没有调用真实云端模型。路由误删恢复、续期不重写、团队/角色/
模型/账号/会话变更、固定发布到期、控制面恢复与授权服务故障均通过。
上游只验证凭据匹配布尔值，不回显密钥或会话令牌；临时集群和运行时凭据已清理。

该组合运行使用 [PR #358](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/358)
的隔离业务验收框架与本阶段本地测试适配，不是默认 CI 自动执行的场景。
本 PR 内的可重复自动回归是上述 Go/envtest 与部署清单测试；后续统一网关
验收入口须在该框架落地主分支后独立接入，避免重复提交尚未合并的测试代码。
这 20 项不包括完整保留头清洗、可信配额身份、员工身份、多模态、容量或生产
全实例采用观测，也不补造缓存命中见证；`adoptionGate` 仍为 false。

回退时先停止实验流量、隔离入口；通过既有业务撤销/发布链路使实验会话的
模型集为空，保持 Operator、权限和源 CR 可用，等待选择路由实际回收。
需要删除源 CR 时，等待 finalizer 正常完成；禁止批量强删。确认没有残留路由
后移除实验入口和独立 RBAC，再关闭该参数/控制器。渲染参数变化仍需新的
publication。不要在源 finalizer 或入口仍依赖组件时先撤销其权限。
恢复原入口属于独立运维步骤，不能同时运行两个相互覆盖的写入者。
