# Examples

Every example on this page is a **complete, runnable program** (with `package main` and all imports), compile-verified against the published v0.1.0. Copy it into a file and `go run` it.

## Single file download

```go
package main

import (
	"context"
	"fmt"
	"log"

	downloader "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	d, err := downloader.New(downloader.Config{
		URL:         "https://example.com/big.iso",
		OutputPath:  "./big.iso",
		Connections: 8,  // simultaneous connections for this file
		Segments:    16, // byte-range pieces (0 = auto)
		OnProgress: func(p downloader.Progress) {
			fmt.Printf("\r%.1f%%  %.1f MiB/s", p.Percent, p.Speed/1024/1024)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	if err := d.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("\ndone:", d.Progress().OutputPath)
}
```

## Batch download (task queue)

`Queue` downloads many files with bounded concurrency. Note that `Concurrency`
(files at once) and each file's `Connections`/`Segments` are **two independent
dimensions**.

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	downloader "github.com/xiaoLangZe/go-muyuan"
)

func main() {
	urls := []string{
		"https://example.com/a.iso",
		"https://example.com/b.iso",
		"https://example.com/c.iso",
	}

	q, err := downloader.NewQueue(downloader.QueueConfig{
		Concurrency: 3, // files downloaded simultaneously
		OutputDir:   "./downloads",
		MaxRetries:  2,               // per-task retries
		RetryDelay:  2 * time.Second, // wait before a retry
		Template: downloader.Config{
			Workers: 16, // per-file (connections + segments) total budget
		},
		OnTaskStateChange: func(t downloader.TaskInfo) {
			fmt.Printf("[%s] %s\n", t.ID, t.State)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer q.Close()

	// An empty path derives the file name from the URL into OutputDir
	ids, err := q.AddAll(urls, "")
	if err != nil {
		log.Fatal(err)
	}

	if err := q.Start(context.Background()); err != nil {
		log.Fatal(err)
	}

	// Tasks can still be added, concurrency changed, one task paused, while running
	if _, err := q.Add("https://example.com/late.iso", ""); err != nil {
		log.Printf("adding task: %v", err)
	}
	q.SetConcurrency(5)
	if len(ids) > 0 {
		_ = q.PauseTask(ids[0]) // sticky: a queue Resume will not revive it
	}

	// Wait returns the aggregate of all task failures
	if err := q.Wait(); err != nil {
		var te *downloader.TaskError
		if errors.As(err, &te) {
			log.Printf("task failed: %s (%s): %v", te.ID, te.URL, te.Err)
		} else {
			log.Printf("queue error: %v", err)
		}
	}

	s := q.Summary()
	fmt.Printf("%d total: %d completed, %d failed, %d canceled, %d paused, %d bytes downloaded\n",
		s.Total, s.Completed, s.Failed, s.Canceled, s.Paused, s.DownloadedBytes)
}
```

### What this example demonstrates

| Code | Meaning |
| --- | --- |
| `Concurrency: 3` | 3 files at once; with 8 connections each that is up to 24 connections |
| `Template.Workers: 16` | each file uses total-budget mode, auto-allocating connections and segments |
| `AddAll(urls, "")` | an empty path derives the name from the URL into `OutputDir`, de-duplicated |
| `q.Add(...)` after `Start` | tasks may be added while the queue runs; scheduled as slots free up |
| `q.PauseTask(ids[0])` | per-task pause is **sticky**; a queue-level `Resume` will not revive it |
| `errors.As(err, &te)` | `Wait` returns an `errors.Join` of `*TaskError`, one per failed task |
| `q.Summary()` | aggregate counts and byte totals for the final report |

If you do not need error attribution, `log.Fatal(q.Wait())` is enough — it is
non-nil whenever any task failed, so partial failures cannot pass silently.

## End-to-end trial run

Serve a Range-capable source locally with the bundled file server — no internet needed:

```bash
# Terminal 1: create a test file, then serve it locally
mkdir -p testdata && dd if=/dev/zero of=testdata/file.bin bs=1M count=50
go run ./examples/testsrv -dir ./testdata -addr 127.0.0.1:18080

# Terminal 2: download from it (allow-private is required for loopback)
go run ./examples/basic -allow-private -o ./file.bin -conn 8 -seg 16 \
    http://127.0.0.1:18080/file.bin

# Or download a whole batch at once
go run ./examples/batch -allow-private -o ./downloads -c 3 -conn 8 \
    http://127.0.0.1:18080/a.bin http://127.0.0.1:18080/b.bin
```

## Interactive examples in the repository

The two programs above are minimal, documentation-oriented versions. The examples
in the repository are interactive CLIs with live status displays that accept
commands on stdin while running:

- [`examples/basic`](https://github.com/xiaoLangZe/go-muyuan/tree/main/examples/basic) — single-file download with a live progress bar; commands `p` (pause) / `r` (resume) / `conn <n>` / `seg <n>` / `workers <n>` / `restart` / `clear` / `q`.
- [`examples/batch`](https://github.com/xiaoLangZe/go-muyuan/tree/main/examples/batch) — batch download via the task queue, with a multi-line status panel and per-task commands such as `pause` / `resume` / `restart` / `cancel <id>`.
- [`examples/testsrv`](https://github.com/xiaoLangZe/go-muyuan/tree/main/examples/testsrv) — a local Range-capable file server for end-to-end trials.
