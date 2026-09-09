# 离线 License E2E 边界

完整的离线 License E2E 只允许在受控开发机或客户隔离内网执行：

```bash
npm ci
npm run test:e2e:offline-license
```

脚本在 `CI=true` 时默认拒绝运行，除非显式设置
`AEP_ALLOW_OFFLINE_LICENSE_E2E=1`。云端 CI 只运行静态
`npm run license:boundary:check`，不会接收 License 签名器、License 私钥、
签名器检出目录或客户 License。

仓库内的 fixture 只有一个测试 License envelope 和对应的 Ed25519 公钥。
生成 fixture 时使用的 License 签名私钥从未落盘到本仓库。E2E 在进程内只
临时生成 AEP JWT 测试 seed，用于本地服务启动，不属于 License 签发私钥。

Compose overlay 以只读方式挂载 fixture，并将 PostgreSQL、MinIO 和 Control
Service 放入内部 Docker 网络。测试会确认网络无外网出口、激活请求体为 `{}`
而不包含 License 材料、服务重启后仍可激活，并确认篡改或部署不匹配的
License 在启动阶段被拒绝，同时扫描服务日志中的 License 和密钥材料。

严禁将客户 License、签名器目录、私钥、签发配置或未脱敏签发日志加入本仓库；
这些材料必须留在批准的离线签发环境。
