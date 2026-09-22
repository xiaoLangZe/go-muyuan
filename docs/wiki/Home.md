# go-muyuan

**English** · [中文](Home-zh)

An HTTP download library for Go. One downloader manages many files over a shared
connection budget; a single file can be fetched over several connections at once.
Every file is a task with a handle that can be paused, resumed, restarted and
cancelled, and progress can be followed live.

```go
m := muyuan.New(
	muyuan.WithThreads(16),  // connections shared by every task
	muyuan.WithMaxFiles(3),  // files downloading at the same time
	muyuan.WithDefaultDir("downloads"),
)
defer m.Close()

task, err := m.AddTask("downloads", "https://example.com/big.iso")
if err != nil {
	return err
}
for ev := range task.Events() { // live progress, no polling
	fmt.Printf("%s %.1f%%\n", ev.Status, ev.Progress.Percent)
}
fmt.Println("saved to", task.FilePath())
```

## Features

- **Several connections per file.** The file is split into pieces fetched in
  parallel, so one slow connection does not cap the transfer.
- **A shared connection budget across files.** The manager divides one pool of
  connections between the running files, breadth first. See
  [Scheduling and Budget](Scheduling-and-Budget).
- **Automatic piece sizing.** The length of one piece is steered towards a target
  duration, so a fast server gets long requests and a slow one short requests.
- **Live progress without polling.** An event stream carries throttled progress,
  every state change, and the error a task ended with. See
  [Progress and Events](Progress-and-Events).
- **Proxy support.** `http`, `https` and `socks5`, with or without credentials.
  See [Proxy and Security](Proxy-and-Security).
- **Resumable.** Pausing and resuming continues from what is already on disk, with
  no dependence on the server answering `If-Range`.
- **No half-finished files.** Bytes go to a `.part` file that is renamed only once
  the transfer is complete.
- **Transient failures are retried**, and a stalled connection is detected.
- **Internal addresses are refused** before the request is built and again when
  the connection is dialled.
- **Zero third-party dependencies.** Standard library only.

## Contents

| Page | What it covers |
| --- | --- |
| [Getting Started](Getting-Started) | Install it, download a first file, read the result |
| [Scheduling and Budget](Scheduling-and-Budget) | How connections are divided between files |
| [Parallel Downloads](Parallel-Downloads) | Pieces, granules, and how the piece length is chosen |
| [Task Control](Task-Control) | Pause, resume, restart, delete — and what happens to the bytes |
| [Progress and Events](Progress-and-Events) | The event stream and polling |
| [Proxy and Security](Proxy-and-Security) | Proxies, and what the host guard does and does not cover |
| [Robustness](Robustness) | Retries, stall detection, partial files, naming |
| [API Reference](API-Reference) | Every option, method, type and error |
| [Recipes](Recipes) | Copy-paste answers to common tasks |
| [Architecture](Architecture) | Package layout and why there are two transfer paths |
| [Limitations and Roadmap](Limitations-and-Roadmap) | What is not implemented yet |
| [FAQ](FAQ) | Real gotchas, with explanations |

## Requirements

Go 1.21 or later. No dependencies outside the standard library.

## Licence

MIT — see [LICENSE](https://github.com/xiaoLangZe/go-muyuan/blob/main/LICENSE).
