# 生产数据面部署

`deploy/kubernetes/production` 是 AEP control-service、gateway-authorizer 和 gateway-reconciler 的生产部署基线，目标是已通过 Helm 部署 Higress 的 Kubernetes 集群。`higress-standalone` 仅用于本地 Compose 和 CI 夹具。

## 前置条件

- Kubernetes 对 `aep-system` 强制 restricted Pod Security Standard。
- `higress-system` 中存在已固定版本的生产 Higress Helm Release；在交付系统固定 Chart 与镜像 digest 后，使用 [higress-values.yaml](../deploy/kubernetes/production/higress-values.yaml)。
- cert-manager 提供名为 `production-acme` 的生产 `ClusterIssuer`，或等效证书流程创建 `aep-public-tls`。
- External Secrets Operator 与名为 `production-secrets` 的 `ClusterSecretStore`；将 [external-secrets.yaml](../deploy/kubernetes/production/external-secrets.yaml) 中的示例远程键映射到企业 Secret Manager。
- 托管 PostgreSQL 与兼容 S3 的对象存储；上线前通过经过评审的 overlay 更新 endpoint、issuer、tenant、域名、镜像引用和资源限额。

仓库不会保存 Kubernetes `Secret` 数据、供应商 API Key、签名 seed、Credential keyring 或初始管理员密码。control-service 与 reconciler 都从挂载文件读取共享数据面 token，不会把它放入 Pod 环境变量。

## 部署与验证

先渲染：

```sh
kubectl kustomize deploy/kubernetes/production
```

镜像、域名、Secret Store 映射和外部 endpoint 获批准后，再部署基线：

```sh
kubectl apply -k deploy/kubernetes/production
kubectl -n aep-system rollout status deployment/aep-control-service
kubectl -n aep-system rollout status deployment/aep-gateway-authorizer
kubectl -n aep-system rollout status deployment/aep-gateway-reconciler
```

两个 reconciler 副本各自拥有临时渲染目录，并配置了 PDB。本 PR 中它们仍只渲染确定性的 Higress 资源，尚未直接 apply Kubernetes 资源。下一项数据面自动化门禁会实现具备 leader 安全性的 server-side apply，并以 Higress 兼容 Kubernetes 资源验证。因此当前版本的 `ready` 状态不能视为线上 Higress 配置已修改的证据。

reconciler 的 Role 和 RoleBinding 明确限定在 `higress-system`，下一阶段可在不授予宽泛集群权限的前提下启用 server-side apply。启用前必须确认已安装 Higress CRD 的组和资源名为 `extensions.higress.io/wasmplugins`。

## 回滚

仅在旧镜像兼容当前只向前迁移的 schema 时，才使用之前的不可变镜像 digest。通过交付系统回滚 manifests 和 Helm Release；若 schema 兼容性不确定，则从同一协调恢复点恢复 PostgreSQL、对象存储、签名 seed 与 Credential keyring。详细流程见 [production-runtime.zh-CN.md](production-runtime.zh-CN.md)。
