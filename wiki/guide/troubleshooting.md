# 常见问题

## `go get` 报 404 或 `Repository not found`

- 确认仓库路径拼写正确：模块路径是 `github.com/xiaoLangZe/go-muyuan`，
  大小写必须完全一致（Go 模块路径区分大小写）。
- 若走代理（`GOPROXY` 指向 goproxy.cn 等），代理索引可能有延迟；
  `go get ...@v0.2.0` 显式指定版本通常立即生效，而 `@latest` 要等
  代理重新索引。

## `go get` 解析到旧的伪版本

无标签时模块只能按提交生成伪版本；伪版本在历史改写后失效。
本项目从 v0.1.0 起打语义化标签，请显式使用正式版本：

```bash
go get github.com/xiaoLangZe/go-muyuan@v0.2.0
```

本地残留坏条目时：`go clean -modcache`。

## 下载开始时立即失败：`host resolves to non-public`

这是 SSRF 防护在工作：目标是回环、局域网或保留地址，而没开
`AllowPrivateHost`。两种选择：

- 目标确实是可信的内网/本机服务 → 设置 `AllowPrivateHost: true`。
- 否则说明 URL 配错了——不要为了"修好报错"关掉防护。

## 进度一直停在 0%，最终报错

常见原因是服务器不支持 `Range`（见[架构设计](./architecture#降级不支持区间请求)）
或返回 403/404。用 `Progress().AcceptRanges` 区分：`false` 表示单流
降级。`Wait` 返回的错误里带具体原因。

## 下载完成后 `Wait` 返回校验和不匹配（ErrChecksumMismatch）

设置了 `VerifySHA256` 且摘要不符：产物已被删除，不会留下错文件。
确认摘要来源正确（发布页的 checksums 而非另一文件的），必要时对
正确文件 `sha256sum` 对照。

## 磁盘满了会发生什么？

short write（实际写入少于请求）现在会被检测并让下载失败——不会静默
产出不完整文件。清理磁盘后重跑即可续传。

## 如何限速？

`Config.MaxBytesPerSec`：该文件**所有连接合计**的速率上限（1 秒
突发配额）。注意它运行期不可热重配，修改需重启下载。

## 进度回调多久触发一次？

每 500ms 左右。回调在管理协程上执行，**不能阻塞**——需要耗时处理
时把快照投递给自己的 channel。

## 队列里所有任务都卡在 Paused？

两种"暂停"别混淆：队列挂起（`Pause`/调低并发）可由 `Resume`/复用
slot 恢复；单任务暂停（`PauseTask`）是**粘性**的，必须 `ResumeTask`
或 `CancelTask`。

## 与中文文档的对应关系

每页都有英文镜像（`/en/` 前缀）；补充文档时请同步更新两份。
