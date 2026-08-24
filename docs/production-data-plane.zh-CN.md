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

两个 reconciler 副本各自拥有审计副本目录，并配置了 PDB。两者都使用 field manager `aep-gateway-reconciler` 执行 Kubernetes server-side apply。因为对象名称和内容完全确定，多个副本无需 leader lease 也能安全持有相同写入字段。只有两个线上 Kubernetes 操作均成功后才会上报 `ready`；部分失败会返回 `KUBERNETES_APPLY_FAILED`，并按有界指数退避重试。

reconciler 的 Role 和 RoleBinding 明确限定为 `higress-system` 中的 Ingress 与 `extensions.higress.io/wasmplugins`。service-account token 与集群 CA 来自 Kubernetes 投射文件。上线前必须确认已安装 Higress CRD 的组和资源名。

运行 `npm run test:e2e:m3-data-plane` 验证控制面与故障收敛，运行 `npm run test:e2e:m3-kubernetes` 验证真实 Kubernetes API Server 与 Higress 兼容 CRD 门禁。

## 回滚

仅在旧镜像兼容当前只向前迁移的 schema 时，才使用之前的不可变镜像 digest。通过交付系统回滚 manifests 和 Helm Release；若 schema 兼容性不确定，则从同一协调恢复点恢复 PostgreSQL、对象存储、签名 seed 与 Credential keyring。详细流程见 [production-runtime.zh-CN.md](production-runtime.zh-CN.md)。
