# 代理与安全

[English](Proxy-and-Security) · **中文**

两个独立的话题，在一个地方交汇：代理会改变连接实际拨向哪个地址，而主机校验检查的正是这个。

## 代理

```go
// 全部任务：
m := muyuan.New(muyuan.WithDefaultProxy("http://user:pass@proxy.example:8080"))

// 或者单条任务：
task, err := m.AddTask(dir, url, muyuan.WithProxy("socks5://user:pass@proxy.example:1080"))
```

支持 **`http`、`https`、`socks5`** 三种 scheme，账号密码写在 URL 的 userinfo 里。其他 scheme 会在 `AddTask` 时被 `ErrInvalidProxy` 拒绝，缺 host 的 URL 同样。

```go
if errors.Is(err, muyuan.ErrInvalidProxy) {
	// 代理 URL 不可用
}
```

分片模式下**每一条连接都经代理** —— 各分片共用同一个 transport，不需要额外配置。

不涉及任何第三方依赖：SOCKS5 由标准库的 HTTP transport 直接处理。

## 主机校验做了什么

交给下载器的 URL 可能来自配置文件、用户输入或远端 API，因此它可能指向本机、或只有内网才能访问的服务。两层机制拦住它：

1. **发出请求之前**，解析目标的 host，并检查它解析出的**全部**地址。解析到被拦地址的主机名会被拒绝，因为最终连哪个地址不由客户端决定。
2. **建立连接时**，再检查一次地址。这堵住了「同一域名第一次解析到公网、第二次解析到内网」的时间窗。

被拦的集合：环回、私网、链路本地（单播与组播）、未指定、组播，以及这些特殊用途网段 `0.0.0.0/8`、`100.64.0.0/10`、`192.0.0.0/24`、`192.0.2.0/24`、`192.88.99.0/24`、`198.18.0.0/15`、`198.51.100.0/24`、`203.0.113.0/24`、`240.0.0.0/4`、`64:ff9b::/96`、`100::/64`、`2001:db8::/32`、`2002::/16`。IPv4-mapped 的 IPv6 地址按它承载的 IPv4 地址判断。

目标 URL 里带账号密码（`https://user:pass@host/`）会被拒绝，因为它们会进入日志和错误信息。需要鉴权请用请求头：

```go
muyuan.WithHeader("Authorization", "Bearer "+token)
```

## 代理改变了校验的什么

这一段要认真读。

| | 不用代理 | 用代理 |
| --- | --- | --- |
| 请求前的 host 校验 | 目标的 host | **目标的 host**（不变） |
| 建连时的地址校验 | 目标解析出的地址 | **代理的地址** |
| 目标解析出的地址 | 会被校验 | **无法校验** —— 解析它的是代理 |

所以：

- **请求前的目标校验照常生效。** 指向 `10.0.0.5` 的 URL 配了代理也照样被拒，与不配代理时一致。
- **建连时校验的对象变成代理**，这也是内网代理默认连不上的原因。自己网络内的代理需要 `WithAllowUnsafeHosts(true)`。
- **经代理时无法校验目标解析出的 IP。** 解析域名的是代理，客户端看不到地址。这是协议本身的属性，不是功能缺失；剩下的是请求前那一层。

## WithAllowUnsafeHosts

```go
muyuan.WithDefaultAllowUnsafeHosts(true) // 管理器级
muyuan.WithAllowUnsafeHosts(true)        // 单条任务
```

这个开关把校验关掉 —— **两层都关，目标和代理都关**。它是总开关：没法做到「放行环回上的代理，同时仍然拒绝私网目标」。那需要两个独立的开关，目前没有（见[限制](Limitations-and-Roadmap-zh)）。

正当用途：本机环回上的测试服务器、自己网络内的代理、来自你自己配置因此已知可信的 URL。

```go
// 测试里，每个服务器都在环回上：
m := muyuan.New(muyuan.WithDefaultAllowUnsafeHosts(true))
```

## 与 WithHTTPClient 的关系

调用方自带的 client 拥有自己的 transport，所以：

- **`WithProxy` 对它无效** —— 请直接在那个 client 上配置代理。
- 只有请求前的 host 校验会跑；建连时的校验属于你提供的 transport。
- `WithStallTimeout` 和 `WithResponseTimeout` 同样不适用。

**不要设置 `Client.Timeout`**：那个字段约束的是整个传输而不只是等响应头，会恰好掐掉这个库存在的意义 —— 长下载。

## 相关

- [API 参考](API-Reference-zh) —— 每个选项
- [限制与路线](Limitations-and-Roadmap-zh) —— 代理相关的取舍
- [常见问题](FAQ-zh) —— 用 `httptest` 测试时为什么撞 `ErrBlockedHost`
