// Package cli implements `vivere run`, the command wrapper that reports a
// job's start, exit code, duration and output tail to a heartbeat monitor.
//
// It is the canonical source for both the standalone binary in this repository
// and the `run` subcommand of the Vivere server binary, which imports it.
// Nothing here talks to a database or knows anything about an account: it makes
// HTTP POSTs to a ping URL and otherwise stays out of the command's way.
package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Run implements `vivere run [--url URL] [--timeout D] [--no-start] [--quiet] -- cmd args...`.
// It returns the exit code the process should exit with: the command's own
// exit code, or 2 for usage errors.
func Run(args []string, version string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	url := fs.String("url", os.Getenv("VIVERE_PING_URL"), "monitor ping URL (or env VIVERE_PING_URL)")
	timeout := fs.Duration("timeout", 0, "kill the command after this long (0 = no limit)")
	noStart := fs.Bool("no-start", false, "do not send the start ping")
	quiet := fs.Bool("quiet", false, "do not print vivere's own status lines")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: vivere run [--url URL] [--timeout 10m] [--no-start] [--quiet] -- command [args...]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cmdArgs := fs.Args()
	if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
		cmdArgs = cmdArgs[1:]
	}
	if len(cmdArgs) == 0 {
		fs.Usage()
		return 2
	}
	base := strings.TrimRight(strings.TrimSpace(*url), "/")
	if base == "" {
		fmt.Fprintln(os.Stderr, "vivere run: no ping URL; pass --url or set VIVERE_PING_URL")
		return 2
	}
	say := func(format string, a ...any) {
		if !*quiet {
			fmt.Fprintf(os.Stderr, "[vivere] "+format+"\n", a...)
		}
	}

	if !*noStart {
		if err := ping(base+"/start", nil, version); err != nil {
			say("start ping failed: %v (continuing)", err)
		}
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if *timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	tail := &tailBuffer{max: 16 * 1024}
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.MultiWriter(os.Stdout, tail)
	cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	started := time.Now()
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			if code < 0 {
				code = 1
			}
		} else {
			code = 1
			tail.Write([]byte("\n[vivere] " + err.Error()))
		}
		if ctx.Err() == context.DeadlineExceeded {
			tail.Write([]byte(fmt.Sprintf("\n[vivere] killed after timeout %s", *timeout)))
		}
	}
	if code > 255 {
		code = 255
	}
	body := tail.Bytes()
	summary := fmt.Sprintf("[vivere] %s exited %d after %s", strings.Join(cmdArgs, " "), code, time.Since(started).Round(time.Millisecond))
	if len(body) > 0 {
		body = append(body, '\n')
	}
	body = append(body, []byte(summary)...)
	if err := ping(fmt.Sprintf("%s/%d", base, code), body, version); err != nil {
		say("completion ping failed: %v", err)
	} else {
		say("reported exit %d", code)
	}
	return code
}

func ping(url string, body []byte, version string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * retryUnit)
		}
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "vivere-cli/"+version)
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		last = fmt.Errorf("server responded %d", resp.StatusCode)
		if resp.StatusCode == http.StatusNotFound {
			return last
		}
	}
	return last
}

// retryUnit is the base of the quadratic backoff between ping attempts: one
// second, four seconds. The tests shorten it so they do not wait five seconds
// to watch a retry happen.
var retryUnit = time.Second

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf...)
}
