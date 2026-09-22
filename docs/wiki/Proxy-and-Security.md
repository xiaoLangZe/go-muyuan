# Proxy and Security

**English** · [中文](Proxy-and-Security-zh)

Two separate subjects that meet in one place: a proxy changes which address the
connection is actually dialled to, and that is exactly what the host guard
inspects.

## Proxy

```go
// Every task:
m := muyuan.New(muyuan.WithDefaultProxy("http://user:pass@proxy.example:8080"))

// Or per task:
task, err := m.AddTask(dir, url, muyuan.WithProxy("socks5://user:pass@proxy.example:1080"))
```

Supported schemes are **`http`, `https` and `socks5`**. Credentials go in the
URL's userinfo. Any other scheme is rejected by `AddTask` with `ErrInvalidProxy`,
as is a URL with no host.

```go
if errors.Is(err, muyuan.ErrInvalidProxy) {
	// the proxy URL is not usable
}
```

In the parallel path **every connection goes through the proxy** — the pieces
share one transport, so there is nothing extra to configure.

No third-party dependency is involved: SOCKS5 is handled by the standard library's
HTTP transport.

## What the host guard does

A URL handed to a downloader may come from a config file, a user, or a remote API,
so it may point at the local machine or at services reachable only from inside the
network. Two layers stop that:

1. **Before the request is built**, the target's host is resolved and every
   address it resolves to is checked. A hostname that resolves to a blocked
   address is refused, because which address gets dialled is not under the
   client's control.
2. **When the connection is dialled**, the address is checked again. This closes
   the window in which a name resolves to a public address once and an internal
   one the next time.

Blocked: loopback, private, link-local (unicast and multicast), unspecified,
multicast, and the special-purpose ranges `0.0.0.0/8`, `100.64.0.0/10`,
`192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`, `198.18.0.0/15`,
`198.51.100.0/24`, `203.0.113.0/24`, `240.0.0.0/4`, `64:ff9b::/96`, `100::/64`,
`2001:db8::/32`, `2002::/16`. IPv4-mapped IPv6 addresses are judged as the IPv4
address they carry.

Credentials in a target URL (`https://user:pass@host/`) are refused, because they
end up in logs and error messages. Use a header instead:

```go
muyuan.WithHeader("Authorization", "Bearer "+token)
```

## What a proxy changes about the guard

This is the part to read carefully.

| | Without a proxy | With a proxy |
| --- | --- | --- |
| Host check before the request | Target's host | **Target's host** (unchanged) |
| Address check at dial time | Target's resolved address | **The proxy's address** |
| Target's resolved address | Checked | **Not checkable** — the proxy resolves it |

So:

- **The pre-request target check keeps working.** A URL pointing at `10.0.0.5`
  is refused with a proxy configured, exactly as without one.
- **The dial-time check now inspects the proxy**, which is what makes an internal
  proxy undialable by default. A proxy inside your own network needs
  `WithAllowUnsafeHosts(true)`.
- **The target's resolved IP cannot be verified through a proxy.** The proxy
  resolves the name, so the client never sees the address. This is a property of
  the protocol, not a missing feature; the pre-request check is what remains.

## WithAllowUnsafeHosts

```go
muyuan.WithDefaultAllowUnsafeHosts(true) // manager-wide
muyuan.WithAllowUnsafeHosts(true)        // one task
```

This turns the guard off — **both layers, for both the target and the proxy**. It
is all-or-nothing: it cannot permit a loopback proxy while still refusing a
private-network target. That would need two separate switches, which do not exist
yet (see [Limitations](Limitations-and-Roadmap)).

Legitimate uses: a test server on loopback, a proxy inside your own network, URLs
that are known to be trusted because they come from your own configuration.

```go
// Tests, where every server is on loopback:
m := muyuan.New(muyuan.WithDefaultAllowUnsafeHosts(true))
```

## Interaction with WithHTTPClient

A caller-supplied client owns its transport, so:

- `WithProxy` **has no effect** on it — configure the proxy on the client instead.
- Only the pre-request host check runs; the dial-time check belongs to the
  transport you provided.
- `WithStallTimeout` and `WithResponseTimeout` do not apply either.

Do not set `Client.Timeout`: that field bounds the whole transfer rather than the
wait for response headers, so it aborts exactly the long downloads this library
exists for.

## Related

- [API Reference](API-Reference) — every option
- [Limitations and Roadmap](Limitations-and-Roadmap) — the proxy caveats in context
- [FAQ](FAQ) — why a test against `httptest` fails with `ErrBlockedHost`
