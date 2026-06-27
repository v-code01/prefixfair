package worker

import (
	"os"
	"testing"
)

// Real-server test defaults. These point at the tiny mmap-shared GGUF and the
// homebrew llama-server locally; both are overridable so the suite is
// portable. When either is missing the integration tests skip rather than fail,
// which keeps the package testable off-box while still exercising a real backend
// wherever one exists.
const (
	defaultBin   = "/opt/homebrew/bin/llama-server"
	defaultModel = "/Users/vanshverma/models/qwen0.5b/qwen2.5-0.5b-instruct-q4_k_m.gguf"
)

// binModel returns the llama-server binary and model paths, honoring env
// overrides, and skips the test if either is absent. Integration tests here spawn
// real processes; there is no simulated fallback, by design.
func binModel(t *testing.T) (bin, model string) {
	t.Helper()
	bin = os.Getenv("PREFIXFAIR_LLAMA_BIN")
	if bin == "" {
		bin = defaultBin
	}
	model = os.Getenv("PREFIXFAIR_MODEL")
	if model == "" {
		model = defaultModel
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("llama-server binary not found at %s; skipping real-backend test", bin)
	}
	if _, err := os.Stat(model); err != nil {
		t.Skipf("model not found at %s; skipping real-backend test", model)
	}
	return bin, model
}
