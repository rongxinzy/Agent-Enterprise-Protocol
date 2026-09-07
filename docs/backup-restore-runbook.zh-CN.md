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
