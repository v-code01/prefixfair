// Package worker is the realness anchor of prefixfair: a thin adapter over real
// llama-server processes. Every latency and every cache-reuse count it returns
// comes from a real server slot doing real prefill work. There is no simulated
// latency and no simulated cache anywhere in this package.
//
// The adapter's one hard rule is honesty about failure: a request that the
// server rejects, or that yields no first token, returns an error and never a
// fabricated datapoint. TTFT is defined only when a real first token arrives, so
// a completion that produced no token has no TTFT to report and is surfaced as an
// error rather than silently counted as a fast zero-token success.
package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Timings mirrors the llama-server completion "timings" object. Only the fields
// load-bearing for the cache mechanism and TTFT cross-check are decoded.
type Timings struct {
	PromptN   int     `json:"prompt_n"`     // prompt tokens actually evaluated (real prefill)
	CacheN    int     `json:"cache_n"`      // prompt tokens reused from the KV cache (skipped)
	PromptMs  float64 `json:"prompt_ms"`    // wall time the server spent in prefill
	PredictMs float64 `json:"predicted_ms"` // wall time the server spent generating
}

// Config describes one llama-server process. Slots is the -np value; the per-slot
// context budget is CtxTotal/Slots and a prompt exceeding it is rejected by the
// server, so callers must keep prompts within PerSlotCtx.
type Config struct {
	Bin        string // path to the llama-server binary
	Model      string // path to the GGUF (mmap-shared across the fleet)
	Port       int    // HTTP port;
	Slots      int    // -np, number of server slots
	CtxTotal   int    // -c, total context split evenly across slots
	Threads    int    // -t and -tb; the fleet pins Sum(threads) <= physical cores
	CacheReuse int    // --cache-reuse min reuse chunk (KV shifting); 0 uses server default
	LogDir     string // directory for the per-port server log
}

// PerSlotCtx is the per-slot context budget. A prompt (plus its generation)
// exceeding this is rejected by the server with exceed_context_size.
func (c Config) PerSlotCtx() int {
	slots := c.Slots
	if slots < 1 {
		slots = 1
	}
	return c.CtxTotal / slots
}

// Request is one completion to send.
type Request struct {
	Prompt      string
	NPredict    int  // tokens to generate; must be >= 1 to define a first token
	CachePrompt bool // allow the slot to reuse KV prefix across requests
}

// Result is one measured completion. TTFT is the wall-clock time from sending the
// request to the first streamed content token, the quantity the whole finding
// rests on. Tokens is the number of content tokens actually received.
type Result struct {
	TTFT    time.Duration
	Timings Timings
	Tokens  int
}

// Worker is a running llama-server process plus a client bound to it.
type Worker struct {
	cfg     Config
	cmd     *exec.Cmd
	log     *os.File
	url     string
	client  *http.Client
	stopped bool
}

// Start spawns one llama-server. It returns as soon as the process is launched;
// callers must WaitHealthy before sending requests and must Stop to reap it.
func Start(cfg Config) (*Worker, error) {
	if cfg.Bin == "" || cfg.Model == "" {
		return nil, fmt.Errorf("worker: Bin and Model are required")
	}
	if cfg.Port == 8080 {
		// 8080 is reserved for a separate local service.
		return nil, fmt.Errorf("worker: port is reserved")
	}
	if cfg.Slots < 1 {
		cfg.Slots = 1
	}
	logDir := cfg.LogDir
	if logDir == "" {
		logDir = os.TempDir()
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("llama-%d.log", cfg.Port))
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("worker: create log %s: %w", logPath, err)
	}

	args := []string{
		"-m", cfg.Model,
		"-np", fmt.Sprint(cfg.Slots),
		"-c", fmt.Sprint(cfg.CtxTotal),
		"-t", fmt.Sprint(cfg.Threads),
		"-tb", fmt.Sprint(cfg.Threads),
		"--metrics",
		"--no-webui",
		"--port", fmt.Sprint(cfg.Port),
		"--host", "127.0.0.1",
	}
	if cfg.CacheReuse > 0 {
		args = append(args, "--cache-reuse", fmt.Sprint(cfg.CacheReuse))
	}

	cmd := exec.Command(cfg.Bin, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("worker: start %s: %w", cfg.Bin, err)
	}

	return &Worker{
		cfg:    cfg,
		cmd:    cmd,
		log:    logFile,
		url:    fmt.Sprintf("http://127.0.0.1:%d", cfg.Port),
		client: &http.Client{Timeout: 180 * time.Second},
	}, nil
}

// URL is the base URL of the server.
func (w *Worker) URL() string { return w.url }

// Port is the server's port.
func (w *Worker) Port() int { return w.cfg.Port }

// Config returns the worker's configuration.
func (w *Worker) Config() Config { return w.cfg }

// Stop kills the process and closes its log. It is idempotent and safe to call
// from a deferred cleanup even if the process already exited.
func (w *Worker) Stop() {
	if w == nil || w.stopped {
		return
	}
	w.stopped = true
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
		_, _ = w.cmd.Process.Wait()
	}
	if w.log != nil {
		_ = w.log.Close()
	}
}

// WaitHealthy polls /health until the server reports ok or the timeout elapses.
// It also returns early with an error if the process exits before becoming
// healthy, so a crashed server is surfaced instead of waited out.
func (w *Worker) WaitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	hc := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		if w.exited() {
			return fmt.Errorf("worker %d: process exited before becoming healthy (see log)", w.cfg.Port)
		}
		resp, err := hc.Get(w.url + "/health")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "ok") {
				return nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("worker %d: not healthy within %s", w.cfg.Port, timeout)
}

// exited reports whether the spawned process has already terminated. Signal 0
// probes liveness without affecting a running process and returns an error once
// the process has finished.
func (w *Worker) exited() bool {
	if w.cmd == nil || w.cmd.Process == nil {
		return true
	}
	return w.cmd.Process.Signal(syscall.Signal(0)) != nil
}

// Tokenize returns the number of tokens the server assigns to content. It is used
// to make cache-hit reasoning concrete (reuse count vs prompt length) and to keep
// prompts inside the per-slot context budget.
func (w *Worker) Tokenize(ctx context.Context, content string) (int, error) {
	body, err := json.Marshal(map[string]any{"content": content})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url+"/tokenize", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("tokenize: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Tokens []int `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return len(out.Tokens), nil
}

// Complete sends a streaming completion and returns the real measured TTFT plus
// the server's timings. It surfaces every failure mode as an error and never
// returns a fabricated datapoint:
//
//   - a non-200 status (for example exceed_context_size on an oversized prompt)
//     returns an error carrying the server's message;
//   - an error object embedded in the SSE stream returns an error;
//   - a stream that ends without ever delivering a content token returns an
//     error, because TTFT is undefined without a first token. This is the exact
//     failure the spike nearly miscounted as a fast zero-token success.
func (w *Worker) Complete(ctx context.Context, r Request) (Result, error) {
	if r.NPredict < 1 {
		r.NPredict = 1
	}
	reqBody, err := json.Marshal(map[string]any{
		"prompt":       r.Prompt,
		"n_predict":    r.NPredict,
		"cache_prompt": r.CachePrompt,
		"stream":       true,
		"temperature":  0.0,
	})
	if err != nil {
		return Result{}, err
	}

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url+"/completion", bytes.NewReader(reqBody))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("completion port %d: %w", w.cfg.Port, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Result{}, fmt.Errorf("completion port %d: status %d: %s",
			w.cfg.Port, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var res Result
	firstStamped := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var chunk struct {
			Content string          `json:"content"`
			Stop    bool            `json:"stop"`
			Timings Timings         `json:"timings"`
			Error   json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A malformed line is not a datapoint; keep scanning for a real one.
			continue
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return Result{}, fmt.Errorf("completion port %d: server error: %s",
				w.cfg.Port, strings.TrimSpace(string(chunk.Error)))
		}
		if chunk.Content != "" {
			if !firstStamped {
				res.TTFT = time.Since(start)
				firstStamped = true
			}
			res.Tokens++
		}
		if chunk.Stop {
			res.Timings = chunk.Timings
		}
	}
	if err := sc.Err(); err != nil {
		return Result{}, fmt.Errorf("completion port %d: stream read: %w", w.cfg.Port, err)
	}
	if !firstStamped {
		// No content token ever arrived, so there is no first-token latency to
		// report. Surface it as a failure rather than inventing a TTFT.
		return Result{}, fmt.Errorf("completion port %d: no tokens generated (no measurable TTFT)", w.cfg.Port)
	}
	return res, nil
}
