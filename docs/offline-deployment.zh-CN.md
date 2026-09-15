# 离线部署 Bundle

仓库可以暂存用于受控内网/隔离网安装的 Docker 镜像 Bundle。Bundle 包含
Compose 输入文件、镜像归档、镜像 ID/digest 和 SHA-256 校验值，不包含
PostgreSQL/MinIO 数据、供应商 API Key、License 私钥、签名 seed、离线制品签名
私钥或客户配置。

## 导出

在可联网的构建机先构建镜像，再执行：

```sh
npm run compose:up
npm run offline:bundle -- --output-dir release/offline-base --profile base
npm run compose:gateway:up
npm run offline:bundle -- --output-dir release/offline-gateway --profile gateway
```

如果 Compose 声明的镜像未存在于本机，命令会失败。命令生成 `manifest.json`
和 `SHA256SUMS` 后以 `awaiting-signature` 状态结束。manifest 会列出所有载荷
文件；checksum 文件覆盖 manifest、安装器、Compose 输入、文档、测试夹具和
全部镜像归档。

不属于本仓库和云端 CI 的获批本地签名器，必须对 `SHA256SUMS` 的准确字节生成
Ed25519 detached signature：

```sh
openssl pkeyutl -sign -rawin \
  -inkey /secure/offline-release.private.pem \
  -in release/offline-base/SHA256SUMS \
  -out release/offline-base/SHA256SUMS.sig
```

每个 profile 都应分别签名，并使用单独保管的公钥完成校验后才能打包。不得发布
未签名的暂存目录。

## 隔离网安装

受信 Ed25519 公钥必须通过不同于 Bundle 的渠道预置到目标机。执行任何 Bundle
内代码前，先使用宿主机可信工具验证 checksum 签名并校验全部载荷：

```sh
openssl pkeyutl -verify -pubin \
  -inkey /etc/aep/offline-release.pub.pem \
  -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig
sha256sum --check SHA256SUMS
```

随后运行 Bundle 自带的无依赖安装器。安装器会再次完成签名和全文件校验，再执行
离线 `docker load`、启动 Compose，并等待 control-service 就绪：

```sh
node install-offline-bundle.mjs \
  --trusted-public-key /etc/aep/offline-release.pub.pem \
  --project aep-offline --port 8080
```

安装器会拒绝使用 Bundle 目录内的公钥，因为它不构成独立信任锚。追加
`--dry-run` 可查看计划而不修改 Docker 状态。仅使用 base Bundle 时安装器会自动省略 gateway 配置。生成的
`offline.yaml` 会移除构建上下文，并固定使用 Bundle 中已加载的 AEP 服务镜像。PostgreSQL、MinIO、部署
Secret、License 和供应商凭据仍由部署方通过离线 Secret 流程单独提供。

该 Bundle 解决镜像/安装介质传输，不等同于升级机制。升级前应按
`backup-restore-runbook.md` 完成 PostgreSQL 与 MinIO 协调备份，确认 schema
兼容性，再加载新 Bundle 并依照 `production-runtime.md` 滚动更新服务。
