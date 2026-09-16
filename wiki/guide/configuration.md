# 配置选项

## Config 字段

`Config` 描述一次单文件下载（完整参考见 [pkg.go.dev](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan)）：

| 字段 | 含义 | 默认值 |
| --- | --- | --- |
| `URL` | 源地址（http/https；会校验 host） | 必填 |
| `OutputPath` | 最终输出路径 | 必填 |
| `Workers` | （连接 + 切片）总和预算；`0` = 传统分别设置模式 | `0` |
| `Connections` | 下载该文件的并发 HTTP 连接数（`Workers>0` 时自动分配） | `8` |
| `Segments` | 文件切成的字节区间段数；`0` = 自动 | `0`（自动） |
| `PartDir` | 临时文件与元数据所在目录 | `OutputPath` 所在目录 |
| `Headers` | 额外请求头（如 `Authorization`） | 无 |
| `Proxy` | 代理地址（`http`、`https`、`socks5`、`socks5h`） | 无 |
| `HTTPClient` | 自定义 `*http.Client`（设置后 `Proxy` 被忽略） | 由 `Proxy` + SSRF 防护构建 |
| `OnProgress` | 进度回调 | 无 |
| `MinSegmentSize` | 自动切片时的单片下限 | `1 MiB` |
| `AllowPrivateHost` | 允许局域网/回环目标（关闭 SSRF 防护） | `false` |
| `MaxBytesPerSec` | 该文件全部连接合计的速率上限（字节/秒）；`0` = 不限；运行期不可热重配 | `0` |
| `VerifySHA256` | 期望的 SHA-256（64 位十六进制）；非空时完成校验，不匹配则删除产物并以 `ErrChecksumMismatch` 失败 | 空 |

## 四种配置模式

`Connections` 与 `Segments` 是两个独立旋钮；`Workers` 是自动分配两者的总和预算：

- **Workers 总和模式**（*推荐用于最高性能*）—— 只设 `Workers` 一个数；库分配 `Connections` 与 `Segments`，两者之和不超过 `Workers`，并在运行中检测慢分片、自动拆分，让最快的 worker 始终满载。
- **仅连接** —— `Segments` 留 `0`；段数根据连接数与文件大小自动推导。
- **仅切片** —— 设置 `Segments`；`Connections` 保持默认。
- **两者都配** —— 都显式设置（连接数超过段数时，多出的连接会空转，因为一段同一时刻只由一条连接抓取）。

### 如何选择

| 场景 | 建议 |
| --- | --- |
| 想省心、要最高性能 | **Workers 总和模式**：只设 `Workers` |
| 有明确的长连接开销考量 | **仅连接**：设 `Connections`，切片自动 |
| 需要更细的续传粒度 | **仅切片**：设 `Segments` |
| 需要精确控制两维 | **两者都配** |

## 运行时同样可改

上面四种模式在下载运行期间同样可用——对应 `SetWorkers` / `SetConnections` / `SetSegments` / `SetConnectionsAndSegments`，已下载字节会保留。详见[运行时控制](./runtime-control)。`MaxBytesPerSec` 与 `VerifySHA256` **不**参与热重配：需要更改时重启下载。

## 限速与完整性校验

- **`MaxBytesPerSec`** 用令牌桶限制写盘速率，是所有连接**合计**的上限（含 1 秒突发配额）。字节不会丢失，只会等待；适合"后台静默下载、不给链路打满"的场景。Queue 里经 `Template` 继承。
- **`VerifySHA256`** 在 rename 之前流式计算整个文件的 SHA-256 并对比。不匹配时产物与附属文件会被删除，`Wait` 返回包裹着 `ErrChecksumMismatch` 的错误——不会留下一个错误的文件。配合已知摘要（如发布页提供的 checksums）即可确保完整下载。

## QoS 相关字段

- **`MinSegmentSize`** 控制自动切片的下限。切片数 `0`（自动）时，库会按连接数与文件大小推导段数，但不会让任何一段小于此值（最小 16 KiB）。调大可避免在中等文件上切得过碎。
- **`PartDir`** 允许把 `.muyuan.partial` 与 `.muyuan.meta.json` 放到与最终文件不同的目录（例如把临时文件放在更快的盘、最终文件放在网络存储）。
- **`Headers`** 用于需要鉴权的源，例如 `Authorization`。
- **`Proxy`** 仅支持 `http`、`https`、`socks5`、`socks5h`（`socks5h` 表示由代理解析域名）。注意代理 URL 会以 `allowPrivate=true` 校验（代理本身常在内网）。
