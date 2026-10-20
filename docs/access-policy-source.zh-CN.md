# 原生授权策略文件交付

简体中文 | [English](access-policy-source.md)

此可选部署提供原生授权 Operator 在
[PR #356](https://github.com/rongxinzy/Agent-Enterprise-Protocol/pull/356) 中需要的静态 HTTPS
源。可独立评审合并，但业务流量接入前须先安装 writer。不启用 Authorino，不配置 Higress，
不切换默认 authorizer，不发布 DB 授权，也不冒充全服务进程采用确认。

## 组件与信任边界

使用现成上游组件，不新增自研策略服务器：

- [kiwigrid/k8s-sidecar 2.14.1](https://github.com/kiwigrid/k8s-sidecar/tree/2.14.1)，
  固定源码 `ea5578d71554967babd90d6eb7e244daa5688a71`，仅监听 `aep-system` 内带
  `gateway.aep.zhiyuan.io/access-policy=true` 标签的 ConfigMap，将文件写入本 Pod 的 emptyDir。
- [nginx-unprivileged 1.30.2-alpine](https://github.com/nginx/docker-nginx-unprivileged)，
  仅通过 TLS 提供 `/access-<64位小写hex>.rego`。Nginx 只读挂载策略，关闭目录列表、
  HTTP 缓存和请求访问日志，拒绝写方法。

两个镜像均固定实际拉取的 registry index digest。没有新 Dockerfile、解释器、授权缓存或
DB writer。Sidecar 只复制字节，不解释 Rego，不验证发布，不延长绝对期限；语义仍由
既有 Operator 负责。

独立身份只可 get/list/watch `aep-system` 的 ConfigMap。RBAC 无法按标签限制 list/watch，
因此它也能读取该命名空间其他 ConfigMap；其中不得存凭据。没有 Secret API 权限、写权限
或全局绑定。仅 sidecar 挂载短期投射 API token，仅 Nginx 挂载 TLS 私钥。两个容器均非 root、
只读根文件系统并删除 capabilities。文件权限 `0640` 允许 owner sidecar 更新，Nginx 所见
挂载仍为只读；把 owner 也设成只读会阻止后续更新。空 folder annotation 设置关闭上游
按 ConfigMap 注解覆盖目标目录的能力。

标签只是选择器，不是安全边界。命名空间内 ConfigMap 写入者必须限于已审查的 Operator
和管理员。通用 sidecar 支持 binary 与 `.url` 文件键；禁止不可信主体提供带标签 ConfigMap
或任意键/注解。Operator 仅生成一个受控 `.rego` 键，不得把这一身份复用给租户配置。

## 显式安装前提

默认、生产、Operator 的既有 kustomization 均不引用此目录。显式安装前需要：

1. 部署侧 Secret 系统提供 `aep-system/aep-access-policy-tls`，含 `tls.crt` 和 `tls.key`，
   证书匹配选定服务名，例如 `aep-access-policy-source.aep-system.svc`。此部署不生成或提交私钥。
2. 在每个 Authorino 容器安排可信 CA bundle，保留 Go 原生证书链与主机名验证。使用内部 CA
   时挂载经过审查的完整 bundle，设置 `SSL_CERT_FILE`，保留 issuer/JWKS 所需公共根。
   禁止关闭证书验证。本阶段不部署 Authorino 或其 CA overlay。
3. CNI 实际执行 NetworkPolicy。这里只允许同命名空间、标签
   `gateway.aep.zhiyuan.io/policy-reader=true` 的 Pod 访问 8443；保护该标签与命名空间 Pod
   创建权限。已有宽泛策略可能因规则叠加扩大访问，安装前需核对。
4. 根据现网出口策略允许 watcher 访问 Kubernetes API。本清单不增加宽泛 egress allow，
   不暴露 NodePort/Ingress；API token 与 CA 验证沿用原生机制。

```sh
kubectl kustomize deploy/kubernetes/access-policy-source
kubectl apply -k deploy/kubernetes/access-policy-source
kubectl -n aep-system rollout status deployment/aep-access-policy-source
```

另行审查的 Operator 设置
`AEP_GATEWAY_ACCESS_POLICY_BASE_URL=https://aep-access-policy-source.aep-system.svc`。
安装文件源不等于启用授权控制器；writer RBAC、原生 AuthConfig CRD 仍须分别显式安装。

两个文件源副本独立 watch，提供 PDB。Sidecar 原生健康端点检查初次同步与 API/watch
新鲜度，只用于 readiness/startup，不因下游 API 不可用就通过 liveness 重启健康进程。
上游 watcher 线程退出会导致进程自行退出。Nginx 使用独立健康探针。Pod 就绪不代表
某文件版本或 Authorino 进程已采用配置。

TLS Secret 投影不会让 Nginx 自动重载证书。交付系统必须在验证证书轮换后滚动文件源，
并监控到期。可用
`kubectl -n aep-system rollout restart deployment/aep-access-policy-source`，按正常双副本
滚动策略保留可用性；这是显式运维要求，不是已实现自动轮换验收。

## 故障语义与验证

原生 watch 动态交付新建、更新、删除的标签文件，新增绑定不用补 volume 列表或滚动 Pod。
文件源重启从当前 ConfigMap 重建 emptyDir。每 Pod 策略卷限 64 MiB，容器有显式内存限制；
这是小规模基线，上线规模必须根据 grant 数量与文件大小实测。

上游 sidecar 原位写文件，没有原子 rename，更新瞬间可能读到不完整内容。Authorino 必须
在下载/编译失败时保留最后良好模块，并让 Operator 不缓存的绝对期限继续约束它；重试、
健康 Pod 或旧模块都不能延长旧发布。文件源副本可能暂时提供不同版本，不存在跨副本线性
发布屏障。全服务进程观察、故障恢复、完整 DB→原生运行时验收仍是后续门禁，
`adoptionGate=false`。

显式测试仅创建并清理自己的新 Kind，使用实际渲染清单、固定上游容器、一次性 CA/服务端
证书及严格 TLS 客户端；逐副本验证动态文件增改删、只读权限、路径/方法限制与副本恢复。
默认 Kind 不执行 NetworkPolicy，报告明确不宣称网络隔离已验收。没有业务 DB、Higress、
Authorino 或数字员工运行时。

```powershell
$env:KIND = 'path/to/kind'
$env:OPENSSL = 'path/to/openssl'
$env:AEP_POLICY_SOURCE_REPORT = 'path/outside/repository/report.json'
node tests/e2e/access-policy-source.mjs
```

Docker 先缓存两个固定 tag 与脚本的固定 Kind 节点镜像。导入 linux/amd64 内容时保留原始
OCI index/digest，不用可变 tag 替换固定引用。生成私钥和镜像归档仅在自有临时目录存放，
`finally` 清理；报告不含凭据。
