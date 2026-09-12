# vivere-cli

`vivere run` wraps a scheduled command and reports what happened to a
[Vivere](https://vivere.dev) heartbeat monitor: that it started, what it exited with, how
long it took, and the last 16 KB of its output.

A cron line that fails loudly is easy. The failure that costs you is the one where nothing
runs at all: the crontab was edited and the job silently dropped, the container was
restarted mid-schedule, the timer unit was masked by a package upgrade, the disk filled and
`cron` could not fork. There is no error to notify you about, because there is no run. A
heartbeat monitor notices the absence instead, and this binary is the part that does the
reporting.

```sh
vivere run --url https://vivere.dev/p/YOUR-KEY -- /opt/backup.sh --full
```

The command's stdout and stderr pass straight through, and `vivere run` exits with the
command's own exit code, so cron, systemd and CI see exactly what they would have seen
without it.

## Install

Single static binary, no dependencies, no runtime.

```sh
# Linux x86-64
curl -fsSL -o /usr/local/bin/vivere https://vivere.dev/static/dl/vivere-linux-amd64
chmod +x /usr/local/bin/vivere
```

The other builds are `vivere-linux-arm64`, `vivere-darwin-arm64`, `vivere-darwin-amd64` and
`vivere-windows-amd64.exe`, in the same directory.

With a Go toolchain:

```sh
go install github.com/nicholasbergesen/vivere-cli/cmd/vivere@latest
```

## Use

```sh
# the URL can come from the environment instead of a flag
export VIVERE_PING_URL=https://vivere.dev/p/YOUR-KEY
vivere run -- python etl.py

# kill a runaway job and report it as failed
vivere run --timeout 30m -- ./slow-job.sh

# in a crontab, where PATH is minimal, use full paths
0 2 * * * /usr/local/bin/vivere run -- /opt/backup.sh
```

| Flag | Effect |
| --- | --- |
| `--url` | Ping URL. Defaults to `$VIVERE_PING_URL`. |
| `--timeout` | Kill the command after this long. It is reported as failed, and the log says why it died. |
| `--no-start` | Skip the start ping. No duration is recorded. |
| `--quiet` | Do not print vivere's own status lines to stderr. The command's output is untouched. |

## What it sends

Two HTTP POSTs, both to the ping URL you configured:

1. `POST <url>/start` with an empty body, before the command runs.
2. `POST <url>/<exit-code>` afterwards, with the last 16 KB of the command's combined
   stdout and stderr, followed by a summary line.

Nothing else leaves the machine. There is no telemetry, no environment capture and no
hostname lookup. The `User-Agent` is `vivere-cli/<version>`.

The output tail is the one thing worth thinking about before you enable it: whatever your
job prints is what gets stored and shown in the run log, so a job that echoes secrets will
send them. Use `--quiet` if that is a concern and keep the job itself quiet, or point it at
a monitor nobody else can read.

## What it does when the network is against it

Pings are retried three times with a short backoff and then given up on. A ping that fails
is reported on stderr and changes nothing else: the command still runs, its output still
passes through, and its exit code is still what this process returns. A monitoring tool that
can fail your backup is worse than no monitoring tool.

A 404 is not retried, because it means the monitor does not exist.

## Building

```sh
go build ./cmd/vivere
go test ./...
```

The tests run the wrapper against a real HTTP server and assert on what actually arrives,
including the retry behaviour and the exit code passed back to the caller. The command they
wrap is the test binary re-executing itself, so they need no shell and run the same way on
Windows.

Releases are cross-compiled with the version stamped in:

```sh
go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o vivere ./cmd/vivere
```

## Why this repository exists

The Vivere server binary has a `vivere run` subcommand that does the same job, and it
imports `github.com/nicholasbergesen/vivere-cli/cli` from here rather than keeping a copy.
This repository is the canonical source for both, so the downloadable binary and the
server's subcommand cannot drift apart.

The server itself is closed source. This is the only part that runs on your machines, which
is the part worth being able to read before you install it.

## Licence

MIT. See [LICENSE](LICENSE).
