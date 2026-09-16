# 贡献指南

感谢考虑为 go-muyuan（木鸢）做贡献。库保持**零第三方依赖**；
新增依赖需要有强理由并经过维护者同意。

## 开发环境

- Go ≥ 1.26
- Node.js ≥ 22（仅用于 `wiki/` 文档站）
- `make`（可选；核心命令都在 `Makefile` 中）

## 工作流

1. 从 `main` 拉出新分支。
2. 修改代码，补齐或更新测试。
3. 本地全绿：

   ```bash
   make fmt-check vet test
   # -race 需本机具备 cgo 工具链；CI 上强制运行
   make race
   ```

4. 文档：公开 API 或配置变化时，同步更新
   [wiki](../wiki/) 的**中英两份**对应页面。代码注释用中文。
5. 提交：

   - 提交消息用 `fix:` / `feat:` / `docs:` / `chore:` / `refactor:` 前缀。
   - **不要**把任何 AI 工具或工作流痕迹写进文件、注释或提交消息。
6. 推送并开 PR。CI（gofmt/vet/test-race/lint）必须全绿。

## 测试约定

- 纯算法放对应 `internal/*` 包的白盒测试。
- 集成测试用 `httptest`，禁止真实外网。
- 时间敏感断言必须留宽区间，避免慢 CI 抖动。

## 发布

- 版本号唯一来源是 `internal/transfer/transfer.go` 的 `Version` 常量；
  发布时更新它、`CHANGELOG.md`、README/wiki 中的版本号，然后打
  `git tag vX.Y.Z` 并推送。
- `wiki/**`、README 与 Go 代码在版本字段上不得脱节（见 `CHANGELOG`）。