# Recipes

**English** · [中文](Recipes-zh)

Copy-paste answers to things people actually need.

## Download one file and wait

```go
m := muyuan.New(muyuan.WithThreads(8), muyuan.WithMaxFiles(1))
defer m.Close()

task, err := m.AddTask("downloads", url)
if err != nil {
	return err
}
if err := task.Wait(); err != nil {
	return err
}
fmt.Println(task.FilePath())
```

## Download several files and wait for all of them

```go
m := muyuan.New(muyuan.WithThreads(16), muyuan.WithMaxFiles(4),
	muyuan.WithDefaultDir("downloads"))
defer m.Close()

for _, url := range urls {
	if _, err := m.AddTask("", url); err != nil {
		return err
	}
}
m.Wait()

for _, task := range m.Tasks() {
	if err := task.Err(); err != nil {
		log.Printf("%s failed: %v", task.URL(), err)
		continue
	}
	fmt.Println("saved", task.FilePath())
}
```

## Wait with a deadline

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
defer cancel()

for _, task := range m.Tasks() {
	if err := task.WaitContext(ctx); err != nil {
		return err // context.DeadlineExceeded if time ran out
	}
}
```

## Show a live progress bar

```go
for ev := range task.Events() {
	if ev.Type != muyuan.EventProgress {
		continue
	}
	p := ev.Progress
	filled := int(p.Percent / 2)
	fmt.Printf("\r[%s%s] %5.1f%%  %s/s  %d conn",
		strings.Repeat("=", filled), strings.Repeat(" ", 50-filled),
		p.Percent, human(p.Speed), p.Workers)
}
fmt.Println()
```

## Print a one-line-per-task overview

```go
for _, task := range m.Tasks() {
	p := task.Progress()
	fmt.Printf("%-28s %6.1f%%  %9s / %-9s  %s\n",
		filepath.Base(task.FilePath()), p.Percent,
		human(p.Downloaded), human(p.Total), task.Status())
}
```

## Retry a failed download without losing progress

```go
if task.Status() == muyuan.StatusFailed {
	// Continues from the bytes on disk when the server serves ranges,
	// and starts over when it does not.
	if err := task.Resume(); err != nil {
		return err
	}
}
```

## Give an urgent download the bandwidth

```go
big.Pause()     // its connections move to the other files at once
urgent.Resume() // and it takes them back as they free up
```

## Download one file with the whole budget

```go
// Nothing else competing: the single file gets every connection.
m := muyuan.New(muyuan.WithThreads(16), muyuan.WithMaxFiles(1))
```

## Walk through a queue slowly

```go
// One file at a time, three connections each, in the order added.
m := muyuan.New(muyuan.WithThreads(3), muyuan.WithMaxFiles(1))
for _, url := range urls {
	task, err := m.AddTask("downloads", url)
	if err != nil {
		return err
	}
	if err := task.Wait(); err != nil {
		return err
	}
}
```

## Authenticate with a token

```go
task, err := m.AddTask(dir, url,
	muyuan.WithHeader("Authorization", "Bearer "+token))
```

## Send a custom User-Agent

```go
// There is no dedicated option; a header does the same job.
task, err := m.AddTask(dir, url,
	muyuan.WithHeader("User-Agent", "my-service/1.0"))
```

## Go through a proxy

```go
// Everything:
m := muyuan.New(muyuan.WithDefaultProxy("http://user:pass@proxy:8080"))

// Just this task:
task, err := m.AddTask(dir, url, muyuan.WithProxy("socks5://user:pass@proxy:1080"))
```

## Branch on the kind of failure

```go
err := task.Wait()

switch {
case err == nil:
	// done

case errors.Is(err, muyuan.ErrBlockedHost):
	// the URL points somewhere the downloader refuses to go

case errors.Is(err, muyuan.ErrStalled):
	// the connection went quiet; Resume retries from disk

case errors.Is(err, muyuan.ErrClosed):
	// the manager was closed

default:
	var status *muyuan.HTTPStatusError
	if errors.As(err, &status) {
		fmt.Println("HTTP", status.StatusCode, "retryable:", status.Retryable())
	}
}
```

## Use it in a test

```go
// httptest servers listen on loopback, which the host guard refuses by default.
m := muyuan.New(
	muyuan.WithThreads(4),
	muyuan.WithMaxFiles(2),
	muyuan.WithDefaultDir(t.TempDir()),
	muyuan.WithDefaultAllowUnsafeHosts(true),
)
defer m.Close()

task, err := m.AddTask(t.TempDir(), srv.URL+"/file.bin")
```

## Rate-limit nothing, but stop a download from hanging forever

```go
m := muyuan.New(
	muyuan.WithThreads(8),
	muyuan.WithDefaultStallTimeout(30*time.Second), // no byte for 30s → retry
	muyuan.WithDefaultRetries(5),
	muyuan.WithDefaultRetryDelay(2*time.Second),
)
```

There is no bandwidth limit; see [Limitations and Roadmap](Limitations-and-Roadmap).

## Related

- [API Reference](API-Reference) — the full surface
- [Getting Started](Getting-Started) — the basics these build on
