# 安全策略

## 报告漏洞

请不要在公开 Issue 中报告安全问题。使用本仓库的 [Private vulnerability reporting](https://github.com/SherlockGougou/itemory-agent/security/advisories/new) 私下提交，内容尽量包括：

- 受影响的版本（`/api/v1/health` 返回的 `version`）；
- 复现步骤或概念验证；
- 可能造成的影响。

## 支持的版本

只有最新发布的版本接收安全修复。镜像标签 `1` 始终指向最新发布的版本。

## 安全边界

- 媒体接口（`/api/v1/*` 中的读取类接口）使用 App 配对时签发的设备 Bearer 令牌。
- 管理操作使用控制台管理员会话 Cookie。两套凭据互不相通，App 不接触管理员凭据。
- 媒体目录以只读方式挂载，服务只向 `/data` 写入索引与缓存。
- 服务设计为在局域网内使用，不建议直接暴露到公网。
