# 安全策略

## 支持的版本

| 版本 | 支持状态 |
| --- | --- |
| latest（main 上的下一发布候选） | 安全修复直接落在 main |
| v0.2.x | ✅ 支持 |
| v0.1.0 | ✅ 支持 |

## 报告漏洞

请**不要**公开披露。将漏洞报告发到仓库所有者的邮箱
<mailto:xshaoze@qq.com>，并尽量附上：

- 受影响版本与使用方式（是否设置 `AllowPrivateHost`、是否使用代理）
- 最小复现
- 影响评估

## 声明的安全边界

`go-muyuan` 的 SSRF 防护（`internal/security`）保护调用方免受通过
恶意 URL 接触内网/本机服务。以下情形**不**在防护范围内：

- `AllowPrivateHost: true` 显式关闭防护（设计如此，用于可信 LAN）。
- 自定义 `HTTPClient`（调用方自担安全责任）。
- 被下载的**内容**是否恶意（不是本库的职责）。

其余安全相关约定见 `SECURITY`、`.github/workflows/codeql.yml` 以及
`internal/security` 的包注释。