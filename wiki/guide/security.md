# 安全防护

## SSRF 防护（三层）

本库拒绝 host 为 `localhost`、回环、私有（RFC 1918）、链路本地、多播、未指定及其他保留地址的 URL。防护分三层，逐层收紧：

| 层级 | 函数 | 时机 | 作用 |
| --- | --- | --- | --- |
| 快速校验 | `ValidateURLQuick` | 入队时 | 免 DNS 的快速检查（scheme + 字面 host），适合批量入队 |
| 权威校验 | `ValidateURL` | `New` / `Add` | 解析 DNS 后校验实际地址 |
| 拨号校验 | `GuardedDial` | 每次建立连接 | **再校验解析出的地址**，可防 DNS rebinding |

第三层是关键：一个域名在校验时解析到公网 IP，但真正拨号时可能被重新解析到内网地址（DNS rebinding）。`GuardedDial` 在拨号那一刻对解析结果再查一次，堵住这个窗口。

被拒绝的地址范围包括：回环、私有、链路本地、多播、未指定，以及额外保留段（CGNAT、TEST-NET、ULA、文档地址等）。

## 局域网 / 本机下载

只有在确实要从受信任的局域网或本机服务下载时，才设置 `AllowPrivateHost: true`。这会关闭 SSRF 防护。

```go
d, _ := downloader.New(downloader.Config{
	URL:              "http://127.0.0.1:18080/file.bin",
	OutputPath:       "./file.bin",
	AllowPrivateHost: true, // 关闭 SSRF 防护
})
```

::: warning
`AllowPrivateHost` 会同时关闭校验与拨号两层防护。只在目标完全可信时使用。
:::

## 代理

`Config.Proxy` 支持 `http`、`https`、`socks5`、`socks5h` 四种 scheme，其他 scheme 返回 `errBadProxyScheme`。`socks5h` 表示域名由代理解析。校验由专用逻辑执行：主机不做公网强制（代理常位于内网），scheme 白名单与 HTTP 端点不同。

即使走了代理，`GuardedDial` 仍然挂在 transport 上，所以 SSRF 防护依然生效。

设置了 `HTTPClient` 时 `Proxy` 被忽略——自定义客户端由调用方自己负责安全策略。

## 输出路径安全

未显式指定 `OutputPath` 的任务，其文件名从 URL 推导时只取最后一个路径段，并剥掉目录分隔符、控制字符与文件系统保留字符，因此构造过的 URL 无法逃出 `OutputDir`。

## 元数据安全

`.muyuan.meta.json` 采用原子写入（marshal → 临时文件 → `rename`），并以 `0600` 权限创建，写入中途崩溃不会损坏记录。
