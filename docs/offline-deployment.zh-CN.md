# 离线部署 Bundle

仓库可以导出用于受控内网/隔离网安装的 Docker 镜像 Bundle。Bundle 包含
Compose 输入文件、镜像归档、镜像 ID/digest 和 SHA-256 校验值，不包含
PostgreSQL/MinIO 数据、供应商 API Key、License 私钥、签名 seed 或客户配置。

## 导出

在可联网的构建机先构建镜像，再执行：

```sh
npm run compose:up
npm run offline:bundle -- --output-dir release/offline-base --profile base
npm run compose:gateway:up
npm run offline:bundle -- --output-dir release/offline-gateway --profile gateway
```

如果 Compose 声明的镜像未存在于本机，命令会失败。每个归档都记录在
`manifest.json` 和 `SHA256SUMS` 中，传输时必须保持完整目录结构。

## 隔离网安装

目标机先用组织批准的本地工具校验 SHA-256，然后加载镜像并启动 Compose；
不要使用 `--build`：

```sh
for archive in images/*.tar; do docker load --input "$archive"; done
docker compose -p aep-offline \
  -f deploy/compose/compose.yaml \
  -f deploy/compose/gateway.yaml \
  -f deploy/compose/offline.yaml up -d
```

仅使用 base Bundle 时省略 `gateway.yaml`。生成的 `offline.yaml` 会移除构建
上下文，并固定使用 Bundle 中已加载的 AEP 服务镜像。PostgreSQL、MinIO、部署
Secret、License 和供应商凭据仍由部署方通过离线 Secret 流程单独提供。

该 Bundle 解决镜像/安装介质传输，不等同于升级机制。升级前应按
`backup-restore-runbook.md` 完成 PostgreSQL 与 MinIO 协调备份，确认 schema
兼容性，再加载新 Bundle 并依照 `production-runtime.md` 滚动更新服务。
