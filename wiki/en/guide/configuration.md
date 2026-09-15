# Configuration

## Config fields

`Config` describes one single-file download (see [pkg.go.dev](https://pkg.go.dev/github.com/xiaoLangZe/go-muyuan) for the full reference):

| Field | Meaning | Default |
| --- | --- | --- |
| `URL` | Source URL (http/https; host validated) | required |
| `OutputPath` | Final destination path | required |
| `Workers` | Total budget of (connections + segments); `0` = classic per-knob mode | `0` |
| `Connections` | Concurrent HTTP connections fetching this file (auto-allocated when `Workers>0`) | `8` |
| `Segments` | Byte-range pieces the file is split into; `0` = auto | `0` (auto) |
| `PartDir` | Where the partial file + metadata live | dir of `OutputPath` |
| `Headers` | Extra request headers (e.g. `Authorization`) | none |
| `Proxy` | Proxy URL (`http`, `https`, `socks5`, `socks5h`) | none |
| `HTTPClient` | Custom `*http.Client` (ignores `Proxy` when set) | built from `Proxy` + SSRF guard |
| `OnProgress` | Progress callback | none |
| `MinSegmentSize` | Lower bound for auto segment sizing | `1 MiB` |
| `AllowPrivateHost` | Permit LAN/loopback targets (disables SSRF guard) | `false` |

## The four configuration modes

`Connections` and `Segments` are independent knobs; `Workers` is a total budget that auto-allocates them:

- **workers total-budget** *(recommended for highest performance)* — set `Workers` to a single number; the library allocates `Connections` and `Segments` whose sum stays within `Workers`, and while running it splits slow segments so the fastest workers stay busy.
- **connections only** — leave `Segments` at `0`; the segment count is derived from the connection count and file size.
- **segments only** — set `Segments`; `Connections` stays at its default.
- **both** — set both explicitly (connections above the segment count just idle, since a segment is fetched by one connection at a time).

### Which to choose

| Situation | Suggestion |
| --- | --- |
| Want it simple and fast | **Workers mode**: set `Workers` only |
| Have specific long-connection cost concerns | **Connections only** |
| Need finer resume granularity | **Segments only** |
| Need exact control of both dimensions | **Both** |

## Runtime use of the same modes

All four modes are also available while a download is running — via `SetWorkers` / `SetConnections` / `SetSegments` / `SetConnectionsAndSegments`, with downloaded bytes preserved. See [Runtime Control](./runtime-control).

## Notes on individual fields

- **`MinSegmentSize`** bounds auto segment sizing. With `Segments` at `0` (auto), the library derives the count from connections and file size but never makes a segment smaller than this (minimum 16 KiB). Raise it to avoid over-splitting medium files.
- **`PartDir`** lets the `.muyuan.partial` and `.muyuan.meta.json` live somewhere other than beside the final file (for example, temps on a faster disk, final output on network storage).
- **`Headers`** is for sources that need auth, e.g. `Authorization`.
- **`Proxy`** accepts only `http`, `https`, `socks5`, and `socks5h` (`socks5h` resolves names at the proxy). Note the proxy URL is validated with `allowPrivate=true`, since proxies often live on a private address.
