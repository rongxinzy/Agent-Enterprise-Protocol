# 企业底座发布制品

对 `main` 历史中的提交创建 `aep-v<package-version>` 标签后，发布工作流会执行
完整门禁并创建 AEP foundation GitHub Release。Release 包含：

- CycloneDX 源码 SBOM；
- control-service、gateway-authorizer、gateway-reconciler 三个镜像的
  CycloneDX SBOM；
- `release-manifest.json`，将所有制品名称与 SHA-256 绑定到准确 Git 提交和协议
  版本；
- 覆盖所有制品及 release manifest 的 `SHA256SUMS`。

云端发布工作流只构建 base 与 gateway 离线目录，用于验证未签名的待签输入，
不会打包或发布这些目录，因为生产离线制品签名私钥不会进入 GitHub CI。获批的
本地发布工作站负责签署 `SHA256SUMS`，使用带外公钥复核完整 Bundle，再生成客户
交付包。具体流程见 `offline-deployment.zh-CN.md`。

传输前应下载同一 Release 的全部文件并校验：

```sh
sha256sum --check SHA256SUMS
```

云端工作流不会接收 License 私钥、离线 Bundle 签名私钥、部署 Secret、供应商
凭据或客户 License。离线签名器、私钥和未脱敏日志必须始终位于本仓库与 CI
之外，未签名的 CI 暂存目录不得作为可安装 Bundle 分发。

这些制品构成可校验的发布证据，但不能替代外部安全评审和客户验收。
