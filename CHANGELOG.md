# 变更日志

本项目遵循[语义化版本](https://semver.org/lang/zh-CN/)。所有值得注意的
变更记录于此。版本号字段（`internal/transfer.Version`）与发布标签始终同步。

## [v0.2.0] - 2026-09-17

### 新增

- `Config.MaxBytesPerSec`：限制单文件全部连接合计的下载速率（令牌桶，1 秒突发）。
- `Config.VerifySHA256`：下载完成后校验 SHA-256，不匹配则删除产物并以 `ErrChecksumMismatch` 失败。
- 新增哨兵错误 `ErrChecksumMismatch`。

### 修复

- `DownloadSequential` 缺失 short-write 检查：磁盘写不满时曾静默丢字节仍报成功。
- `Probe` 在 HEAD 未返回 `Content-Length` 时曾把 `-1` 存入 `Total`，破坏"未知为 0"语义。
- 完成路径的 `Sync`/`Close` 错误不再被吞：失败会使下载失败而非假装成功。
- `Restart` 的 `Truncate` 失败不再被吞：失败会使下载失败。
- 队列与下载器的各 `ClearCache` 删除失败现在对调用方可见（任务错误/终态错误）。
- 队列级 `ClearCache` 对运行中任务此前不清理缓存（重启后静默续传），现在会真正从头重下。
- 代理预检误用 `ValidateURL`，导致文档承诺的 `socks5`/`socks5h` 代理在 `New` 阶段被拒；现改用专用的 `ValidateProxyURL`。
- `QueueConfig.Template` 注释中的字段名笔误（`MinSegmentBytes` → `MinSegmentSize`）。

### 工程

- 新增 CI：`ci.yml`（gofmt/vet/`go test -race`/覆盖率上传 Codecov）、golangci-lint、CodeQL、Dependabot。
- 文档站 workflow 增加 PR 构建校验（PR 不部署）。
- 新增 `Makefile`、`.editorconfig`、`.golangci.yml`。
- LICENSE 版权行从占位符改为 `Copyright 2026 xiaoLangZe`。
- 程序包文档补齐 5 个缺失的 `String`/`Error` 方法注释；新增 pkg.go.dev 可运行示例。

### 测试

- `internal/transfer`：覆盖率 0% → 92%。
- `internal/security`：67% → 94%，含拨号级 SSRF（DNS 重绑定防护）注入测试。
- 根包：新增 Config 边界、代理、限速、校验和、生命周期（Close/重启动/ctx 取消）、
  续传拒绝矩阵等测试；修复 `TestQueueCallbacks` 的死断言。

## [v0.1.0] - 2026-09-15

首个发布版本。

### 特性

- 多连接区间下载：N 段切片 × T 条并发连接。
- Workers 总和模式：单一预算自动分配连接与切片。
- 运行中自适应慢分片拆分。
- 批量下载队列（任务级控制、重试、去重输出路径）。
- 运行时热重配（连接/切片，`SetWorkers`/`SetConnections`/`SetSegments`）。
- 跨进程续传（`Content-Length`/`ETag`/`Last-Modified` 校验）。
- 无 Range 服务端的单流降级。
- SSRF 三层防护（快检、DNS 权威校验、拨号时重校验）。
- 代理支持（`http`/`https`/`socks5`/`socks5h`）。
- 进度回调与快照（EWMA 速度与 ETA）。