# 备份恢复演练

备份恢复演练验证 PostgreSQL 管控数据和 MinIO Skill 对象能否一起恢复到
隔离部署。它是一次性集成测试，不会接触默认的 `aep-m0` Compose 项目或其
数据卷。

在仓库根目录执行：

```sh
npm ci
npm run test:e2e:backup-restore
```

场景会创建临时用户、Skill、已发布版本和用户授权，停止应用写入后生成
PostgreSQL custom-format 备份和 MinIO 数据卷归档，再恢复到第二个 Compose
项目。随后验证管理员会话/JWKS 连续性、Skill 元数据、用户清单和下载包的
SHA-256。无论成功或失败，两个项目及其临时数据卷都会在清理阶段删除。

默认端口被占用时，可通过 `AEP_BACKUP_SOURCE_PORT`、
`AEP_BACKUP_RESTORE_PORT`、`AEP_BACKUP_SOURCE_MINIO_PORT` 和
`AEP_BACKUP_RESTORE_MINIO_PORT` 覆盖。不要将这些项目指向现有生产数据库或
对象存储。

该演练可作为 GA 门禁证据，但不能替代部署方的 PostgreSQL/MinIO 定期备份、
Secret Provider 备份，或组织批准的恢复时间目标和恢复点目标。

## 生产操作工具

可以使用仓库内的操作脚本生成可校验的协调备份。脚本会暂时停止
`control-service` 和 MinIO，使用 PostgreSQL custom format 导出数据库，并将
MinIO 数据卷归档到同一目录；完成后自动恢复服务：

```sh
npm run ops:backup -- --project aep-m0 --output-dir backups/20260909
```

输出目录包含 `postgres.dump`、`minio-data.tgz` 和记录大小及 SHA-256 的
`manifest.json`。归档 MinIO 数据需要目标机预置 `alpine:3.20`（可通过
`--helper-image` 指定组织批准的等价镜像）。脚本不会备份数据库密码、部署
Secret、License 材料或供应商凭据。

恢复会覆盖目标项目的数据库和 MinIO 数据卷，必须显式确认：

```sh
npm run ops:restore -- --project aep-m0 --input-dir backups/20260909 --confirm yes
```

恢复前应停止对目标项目的所有写入，并先在隔离项目演练。恢复脚本会重新
校验 `manifest.json` 中的 SHA-256，使用 `--no-build --pull never` 启动服务，
最后等待 `/readyz` 返回成功。若恢复失败，应保持目标项目停止状态并按照
组织的灾备流程从上一恢复点处理；脚本不会自动删除或回滚现有数据。
