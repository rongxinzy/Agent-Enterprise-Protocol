# 企业底座发布制品

对 `main` 历史中的提交创建 `aep-v<package-version>` 标签后，发布工作流会执行
完整门禁并创建 AEP foundation GitHub Release。Release 包含：

- base 与 gateway 两个离线 Compose Bundle；
- CycloneDX 源码 SBOM；
- control-service、gateway-authorizer、gateway-reconciler 三个镜像的
  CycloneDX SBOM；
- `release-manifest.json`，将所有制品名称与 SHA-256 绑定到准确 Git 提交和协议
  版本；
- 覆盖所有制品及 release manifest 的 `SHA256SUMS`。

版本化 AEP 镜像已装入离线 Bundle。本地 Compose gateway 档位使用的第三方镜像
仍在 Bundle 自身的 `manifest.json` 中记录引用与 digest。gateway Bundle 只用于
集成和隔离网验证，不会把 `higress-standalone` 变成获批的生产拓扑。

传输前应下载同一 Release 的全部文件并校验：

```sh
sha256sum --check SHA256SUMS
```

云端工作流不会接收 License 私钥、客户制品签名私钥、部署 Secret、供应商凭据
或客户 License。客户交付需要签名时，由获批的本地签名环境签署审阅后的
`release-manifest.json` 或客户打包清单；签名器、私钥和未脱敏日志继续置于本
仓库及 CI 之外。

这些制品构成可校验的发布证据，但不能替代外部安全评审和客户验收。
