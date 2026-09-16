# Security

## SSRF guard (three layers)

The package rejects any URL whose host is `localhost`, loopback, private (RFC 1918), link-local, multicast, unspecified, or otherwise reserved. The guard has three layers that tighten progressively:

| Layer | Function | When | Purpose |
| --- | --- | --- | --- |
| Quick check | `ValidateURLQuick` | enqueue | DNS-free check (scheme + literal host); good for bulk enqueue |
| Authoritative | `ValidateURL` | `New` / `Add` | Resolves DNS and validates the actual address |
| Dial-time | `GuardedDial` | every connection | **Re-checks the resolved address**, defeating DNS rebinding |

The third layer is the important one: a name can resolve to a public IP at validation time but to an internal address when the connection is actually dialed (DNS rebinding). `GuardedDial` re-checks at dial time, closing that window.

Rejected ranges include loopback, private, link-local, multicast, unspecified, plus extra reserved blocks (CGNAT, TEST-NET, ULA, documentation addresses).

## LAN / local downloads

Set `AllowPrivateHost: true` only when you intend to download from a trusted LAN or local server. This disables the SSRF guard.

```go
d, _ := downloader.New(downloader.Config{
	URL:              "http://127.0.0.1:18080/file.bin",
	OutputPath:       "./file.bin",
	AllowPrivateHost: true, // disables the SSRF guard
})
```

::: warning
`AllowPrivateHost` disables both the validation and dial-time layers. Use it only when the target is fully trusted.
:::

## Proxy

`Config.Proxy` accepts the schemes `http`, `https`, `socks5`, and `socks5h`; any other scheme returns `errBadProxyScheme`. `socks5h` means names are resolved at the proxy. Validation uses dedicated logic: the host is not forced public (proxies commonly sit on private addresses), while the scheme whitelist differs from that of HTTP endpoints.

Even when a proxy is used, `GuardedDial` stays wired into the transport, so the SSRF guard still applies.

Setting `HTTPClient` makes `Proxy` be ignored — a custom client is the caller's own security responsibility.

## Output path safety

For a task with no explicit `OutputPath`, the name derived from the URL takes only the last path component and strips directory separators, control characters, and filesystem-reserved characters, so a crafted URL cannot escape `OutputDir`.

## Metadata safety

`.muyuan.meta.json` is written atomically (marshal → temp file → `rename`) and created with `0600` permissions, so a crash mid-write cannot corrupt the record.
