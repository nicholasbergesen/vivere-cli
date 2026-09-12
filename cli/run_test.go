package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The child process these tests wrap is the test binary itself, re-executed
// with VIVERE_TEST_CHILD set. A shell script would be simpler and would not
// run on Windows, which is one of the five platforms this binary is built for.
func TestMain(m *testing.M) {
	if spec := os.Getenv("VIVERE_TEST_CHILD"); spec != "" {
		os.Exit(runChild(spec))
	}
	os.Exit(m.Run())
}

// runChild interprets a ";"-separated script: "out:text" writes to stdout,
// "err:text" to stderr, "sleep:1s" waits, "exit:3" sets the exit code.
func runChild(spec string) int {
	code := 0
	for _, step := range strings.Split(spec, ";") {
		verb, arg, _ := strings.Cut(step, ":")
		switch verb {
		case "out":
			fmt.Fprint(os.Stdout, arg)
		case "err":
			fmt.Fprint(os.Stderr, arg)
		case "sleep":
			d, err := time.ParseDuration(arg)
			if err != nil {
				return 99
			}
			time.Sleep(d)
		case "exit":
			n, err := strconv.Atoi(arg)
			if err != nil {
				return 99
			}
			code = n
		}
	}
	return code
}

// child returns the argv that re-runs this test binary as the given script.
// The script reaches it through the environment, which the child inherits.
func child(t *testing.T, spec string) []string {
	t.Helper()
	t.Setenv("VIVERE_TEST_CHILD", spec)
	return []string{os.Args[0]}
}

type hit struct {
	path string
	body string
	ua   string
}

// recorder is a stand-in ping endpoint that records what actually arrived.
type recorder struct {
	*httptest.Server
	mu     sync.Mutex
	hits   []hit
	status func(n int) int // status code for the nth request, nil means 200
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		n := len(r.hits)
		r.hits = append(r.hits, hit{path: req.URL.Path, body: string(body), ua: req.Header.Get("User-Agent")})
		status := 200
		if r.status != nil {
			status = r.status(n)
		}
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) all() []hit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hit(nil), r.hits...)
}

func (r *recorder) paths() []string {
	var out []string
	for _, h := range r.all() {
		out = append(out, h.path)
	}
	return out
}

// quiet keeps the command's own output out of the test log; --quiet only
// silences vivere's status lines, not the wrapped command's stdout.
func args(url string, rest ...string) []string {
	return append([]string{"--quiet", "--url", url}, rest...)
}

func TestRunReportsSuccess(t *testing.T) {
	rec := newRecorder(t)
	argv := child(t, "out:hello from the job;exit:0")

	if code := Run(append(args(rec.URL), append([]string{"--"}, argv...)...), "test"); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}

	hits := rec.all()
	if len(hits) != 2 {
		t.Fatalf("got %d pings (%v), want start then 0", len(hits), rec.paths())
	}
	if hits[0].path != "/start" {
		t.Errorf("first ping %q, want /start", hits[0].path)
	}
	if hits[0].body != "" {
		t.Errorf("start ping carried a body: %q", hits[0].body)
	}
	if hits[1].path != "/0" {
		t.Errorf("completion ping %q, want /0", hits[1].path)
	}
	if !strings.Contains(hits[1].body, "hello from the job") {
		t.Errorf("log does not carry the command's stdout: %q", hits[1].body)
	}
	if !strings.Contains(hits[1].body, "exited 0 after") {
		t.Errorf("log has no summary line: %q", hits[1].body)
	}
	if !strings.HasPrefix(hits[1].ua, "vivere-cli/") {
		t.Errorf("user agent %q, want vivere-cli/<version>", hits[1].ua)
	}
}

// The exit code has to reach both the monitor and the caller: cron and CI
// decide whether the job failed from the code this process returns.
func TestRunReportsFailureAndPassesTheCodeThrough(t *testing.T) {
	rec := newRecorder(t)
	argv := child(t, "err:it broke;exit:3")

	if code := Run(append(args(rec.URL), append([]string{"--"}, argv...)...), "test"); code != 3 {
		t.Fatalf("exit code %d, want 3", code)
	}
	hits := rec.all()
	if len(hits) != 2 || hits[1].path != "/3" {
		t.Fatalf("pings %v, want [/start /3]", rec.paths())
	}
	if !strings.Contains(hits[1].body, "it broke") {
		t.Errorf("log does not carry stderr: %q", hits[1].body)
	}
}

func TestRunSkipsStartPing(t *testing.T) {
	rec := newRecorder(t)
	argv := child(t, "exit:0")

	if code := Run(append(args(rec.URL, "--no-start"), append([]string{"--"}, argv...)...), "test"); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	if paths := rec.paths(); len(paths) != 1 || paths[0] != "/0" {
		t.Fatalf("pings %v, want [/0] only", paths)
	}
}

// A command that outlives --timeout is killed and reported as failed, with the
// reason in the log rather than left for someone to guess at.
func TestRunTimeoutKillsAndReports(t *testing.T) {
	rec := newRecorder(t)
	argv := child(t, "out:starting;sleep:30s;exit:0")

	start := time.Now()
	code := Run(append(args(rec.URL, "--timeout", "300ms"), append([]string{"--"}, argv...)...), "test")
	if code == 0 {
		t.Fatal("timed-out command reported success")
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("timeout did not kill the command: took %s", d)
	}
	hits := rec.all()
	last := hits[len(hits)-1]
	if last.path == "/0" {
		t.Fatalf("completion ping %q, want a failure code", last.path)
	}
	if !strings.Contains(last.body, "killed after timeout") {
		t.Errorf("log does not say why it died: %q", last.body)
	}
}

// A ping URL is the one thing the wrapper cannot work without. Getting this
// wrong silently would leave a job looking monitored when it is not.
func TestRunWithoutURLIsAUsageError(t *testing.T) {
	t.Setenv("VIVERE_PING_URL", "")
	argv := child(t, "exit:0")
	if code := Run(append([]string{"--quiet", "--"}, argv...), "test"); code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
}

func TestRunReadsURLFromEnvironment(t *testing.T) {
	rec := newRecorder(t)
	argv := child(t, "exit:0")
	t.Setenv("VIVERE_PING_URL", rec.URL+"/")

	if code := Run(append([]string{"--quiet", "--"}, argv...), "test"); code != 0 {
		t.Fatalf("exit code %d, want 0", code)
	}
	// The trailing slash must not produce "//0".
	if paths := rec.paths(); len(paths) != 2 || paths[1] != "/0" {
		t.Fatalf("pings %v, want [/start /0]", paths)
	}
}

func TestRunWithNoCommandIsAUsageError(t *testing.T) {
	rec := newRecorder(t)
	if code := Run([]string{"--quiet", "--url", rec.URL}, "test"); code != 2 {
		t.Fatalf("exit code %d, want 2", code)
	}
	if paths := rec.paths(); len(paths) != 0 {
		t.Fatalf("a usage error pinged the monitor: %v", paths)
	}
}

// A failing ping is retried, and a job is never failed because of it.
func TestPingRetriesThenSucceeds(t *testing.T) {
	old := retryUnit
	retryUnit = time.Millisecond
	t.Cleanup(func() { retryUnit = old })

	rec := newRecorder(t)
	rec.status = func(n int) int {
		if n == 0 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	}
	if err := ping(rec.URL+"/0", []byte("log"), "test"); err != nil {
		t.Fatalf("ping gave up on a retryable failure: %v", err)
	}
	if n := len(rec.all()); n != 2 {
		t.Fatalf("%d attempts, want 2", n)
	}
}

// 404 means the monitor does not exist, so retrying it is pointless noise.
func TestPingDoesNotRetryNotFound(t *testing.T) {
	old := retryUnit
	retryUnit = time.Millisecond
	t.Cleanup(func() { retryUnit = old })

	rec := newRecorder(t)
	rec.status = func(int) int { return http.StatusNotFound }
	if err := ping(rec.URL+"/0", nil, "test"); err == nil {
		t.Fatal("404 reported as success")
	}
	if n := len(rec.all()); n != 1 {
		t.Fatalf("%d attempts on a 404, want 1", n)
	}
}

// An unreachable monitor must not change what the job did.
func TestRunSurvivesADeadMonitor(t *testing.T) {
	old := retryUnit
	retryUnit = time.Millisecond
	t.Cleanup(func() { retryUnit = old })

	rec := newRecorder(t)
	dead := rec.URL
	rec.Close()

	argv := child(t, "exit:7")
	if code := Run(append(args(dead), append([]string{"--"}, argv...)...), "test"); code != 7 {
		t.Fatalf("exit code %d, want 7 even with no monitor reachable", code)
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	tb := &tailBuffer{max: 8}
	tb.Write([]byte("abcdefghij"))
	if got := string(tb.Bytes()); got != "cdefghij" {
		t.Fatalf("tail %q, want the last 8 bytes", got)
	}
	tb.Write([]byte("KL"))
	if got := string(tb.Bytes()); got != "efghijKL" {
		t.Fatalf("tail %q after a second write", got)
	}
}
