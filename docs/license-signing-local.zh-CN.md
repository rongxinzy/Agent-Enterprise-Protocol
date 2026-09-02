# 企业 License 本地签发边界

企业 License 签名器是离线、受本地控制的组件，故意不放入 AEP 仓库、
Control Service 镜像、Admin Console 或任何云端构建任务。

## 职责

- 签名器只在企业受控的签发环境中持有生产 License 私钥并运行。
- 发布人员在本地生成已签名 License，通过企业批准的分发渠道只传递
  License 结果和公钥验签材料。
- Zhiyuan 企业客户端先在本地验签，再调用
  `POST /aep/v1/agent/activation` 交换 License 证据。
- Control Service 校验激活请求并签发短期 entitlement JWT，绝不接收
  License 私钥，也不执行 License 签名。

## 仓库边界

签名器检出目录应位于本仓库之外，例如：

```text
D:\rxzy\zhiyuan-license-signer\
```

不得将签名器源码、私钥、签发配置或未脱敏签发日志复制到 AEP 工作树。
仓库已忽略常见本地签名器目录和私钥工件名称，作为额外防线；但每次推送
前仍必须由操作人员检查 `git status`。

公钥验签材料可以随客户端发布包或批准的 License 分发系统提供，不能与
私钥混淆。

## 轮换与恢复

在使用旧密钥签发的 License 全部过期前，必须继续保留旧公钥。私钥和签发
元数据应在企业离线 Secret 系统中备份，并与 PostgreSQL、MinIO 和
Credential keyring 分开保存。AEP 备份恢复时，必须先恢复匹配的验签材料，
再重新启用企业客户端。

## 发布门禁

发布 AEP 或 Zhiyuan 企业版前：

1. 确认 Git 未跟踪任何签名器路径或私钥工件。
2. 确认 CI 无法访问签发环境和私钥。
3. 确认客户端能验签测试 License，并在调用激活接口前拒绝被篡改的包。
4. 确认激活响应只包含短期 entitlement JWT。
