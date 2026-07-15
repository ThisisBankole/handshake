// Package authoring runs a configured coding CLI as a bounded background
// writer for Handshake's project-level knowledge documents.
package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"handshake/internal/db"
)

type Runner string

const (
	RunnerClaude   Runner = "claude"
	RunnerCodex    Runner = "codex"
	RunnerOpenCode Runner = "opencode"
	RunnerHermes   Runner = "hermes"
)

const (
	defaultDebounce = 30 * time.Second
	defaultTimeout  = 2 * time.Minute
	defaultRetries  = 3
)

var ErrDisabled = errors.New("knowledge authoring is disabled")

// Config is deliberately local and human-readable. It contains no provider
// credentials; each runner uses the user's existing CLI authentication.
type Config struct {
	Enabled         bool   `json:"enabled"`
	Runner          Runner `json:"runner"`
	DebounceSeconds int    `json:"debounce_seconds"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	RetryLimit      int    `json:"retry_limit"`
}

func DefaultConfig() Config {
	return Config{DebounceSeconds: int(defaultDebounce.Seconds()), TimeoutSeconds: int(defaultTimeout.Seconds()), RetryLimit: defaultRetries}
}

func ConfigPath(homeDir string) string {
	return filepath.Join(homeDir, ".handshake", "authoring.json")
}

func LoadConfig(homeDir string) (Config, error) {
	config := DefaultConfig()
	contents, err := os.ReadFile(ConfigPath(homeDir))
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, fmt.Errorf("read authoring config: %w", err)
	}
	if err := json.Unmarshal(contents, &config); err != nil {
		return config, fmt.Errorf("parse authoring config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return config, err
	}
	return config, nil
}

func SaveConfig(homeDir string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	path := ConfigPath(homeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create authoring config directory: %w", err)
	}
	contents, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode authoring config: %w", err)
	}
	return os.WriteFile(path, append(contents, '\n'), 0600)
}

func (c Config) Validate() error {
	if c.Enabled && !IsSupported(c.Runner) {
		return fmt.Errorf("unsupported knowledge authoring runner %q", c.Runner)
	}
	if c.DebounceSeconds <= 0 || c.DebounceSeconds > 3600 {
		return fmt.Errorf("authoring debounce must be between 1 and 3600 seconds")
	}
	if c.TimeoutSeconds <= 0 || c.TimeoutSeconds > 3600 {
		return fmt.Errorf("authoring timeout must be between 1 and 3600 seconds")
	}
	if c.RetryLimit < 0 || c.RetryLimit > 20 {
		return fmt.Errorf("authoring retry limit must be between 0 and 20")
	}
	return nil
}

func IsSupported(runner Runner) bool {
	return runner == RunnerClaude || runner == RunnerCodex || runner == RunnerOpenCode || runner == RunnerHermes
}

func Runners() []Runner {
	return []Runner{RunnerClaude, RunnerCodex, RunnerOpenCode, RunnerHermes}
}

func Binary(runner Runner) string { return string(runner) }

type DetectedRunner struct {
	Runner Runner
	Path   string
	Err    error
}

func DetectRunners() []DetectedRunner {
	result := make([]DetectedRunner, 0, len(Runners()))
	for _, runner := range Runners() {
		path, err := exec.LookPath(Binary(runner))
		result = append(result, DetectedRunner{Runner: runner, Path: path, Err: err})
	}
	return result
}

// ProbeRunner performs a bounded local CLI check. It deliberately does not
// invoke a model or send project data; authoring jobs provide the real MCP and
// publication verification once a writer is enabled.
func ProbeRunner(ctx context.Context, runner Runner) error {
	if !IsSupported(runner) {
		return fmt.Errorf("unsupported knowledge authoring runner %q", runner)
	}
	path, err := exec.LookPath(Binary(runner))
	if err != nil {
		return fmt.Errorf("%s is not installed or not on PATH: %w", runner, err)
	}
	if err := exec.CommandContext(ctx, path, "--version").Run(); err != nil {
		return fmt.Errorf("%s did not pass its local CLI check: %w", runner, err)
	}
	return nil
}

// BuildCommand converts the common authoring request into each CLI's
// documented non-interactive entrypoint. The prompt deliberately names only
// Handshake-controlled paths and MCP operations; terminal output is ignored.
func BuildCommand(runner Runner, prompt string) (string, []string, error) {
	switch runner {
	case RunnerClaude:
		return Binary(runner), []string{"-p", prompt}, nil
	case RunnerCodex:
		return Binary(runner), []string{"exec", prompt}, nil
	case RunnerOpenCode:
		return Binary(runner), []string{"run", "--auto", prompt}, nil
	case RunnerHermes:
		return Binary(runner), []string{"-z", prompt}, nil
	default:
		return "", nil, fmt.Errorf("unsupported knowledge authoring runner %q", runner)
	}
}

func AuthoringPrompt(projectID string, revision int64, bundleRoot string) string {
	return fmt.Sprintf(`You are Handshake's background knowledge author. Do not edit source files, run destructive commands, or change Git state.

Read the factual knowledge bundle at %q for project %q, revision %d. Follow the installed knowledge-authoring skill. Use Handshake MCP to publish both required documents: project-brief and repo-map. Pass project_id %q and facts_revision %d exactly. Do not merely describe the documents in your final response: publish them with publish_project_knowledge.`, bundleRoot, projectID, revision, projectID, revision)
}

// Invoker is injectable so job lifecycle tests do not need a model account or
// a real coding CLI installed.
type Invoker interface {
	Invoke(ctx context.Context, runner Runner, workingDir, prompt string) error
}

type CLIInvoker struct {
	MCPURL     string
	RuntimeDir string
}

func (invoker CLIInvoker) Invoke(ctx context.Context, runner Runner, workingDir, prompt string) error {
	binary, args, err := BuildCommand(runner, prompt)
	if err != nil {
		return err
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return fmt.Errorf("%s is not installed or not on PATH: %w", binary, err)
	}
	if runner == RunnerClaude && invoker.MCPURL != "" {
		configPath, err := invoker.writeClaudeMCPConfig()
		if err != nil {
			return err
		}
		args = append([]string{"--mcp-config", configPath, "--strict-mcp-config"}, args...)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = workingDir
	cmd.Env = os.Environ()
	if runner == RunnerOpenCode {
		// OpenCode needs approval to read Handshake's bundle outside the project.
		// Auto-approval is safe here because source edits and shell commands are
		// explicitly denied for this authoring-only process.
		cmd.Env = append(cmd.Env, `OPENCODE_PERMISSION={"edit":"deny","bash":"deny","external_directory":"allow"}`)
	}
	// The process receives only its normal agent credentials/configuration.
	// Session transcripts are never passed as command arguments.
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
		if output != "" {
			return fmt.Errorf("%s authoring run: %w: %s", runner, err, output)
		}
		return fmt.Errorf("%s authoring run: %w", runner, err)
	}
	return nil
}

func (invoker CLIInvoker) writeClaudeMCPConfig() (string, error) {
	if invoker.RuntimeDir == "" {
		return "", fmt.Errorf("Claude authoring requires a runtime directory")
	}
	if err := os.MkdirAll(invoker.RuntimeDir, 0700); err != nil {
		return "", fmt.Errorf("create Claude runtime directory: %w", err)
	}
	contents, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"handshake": map[string]string{"type": "http", "url": invoker.MCPURL},
	}})
	if err != nil {
		return "", fmt.Errorf("encode Claude MCP config: %w", err)
	}
	path := filepath.Join(invoker.RuntimeDir, "claude-mcp.json")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		return "", fmt.Errorf("write Claude MCP config: %w", err)
	}
	return path, nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const max = 4096
	remaining := max - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			b.Buffer.Write(p[:remaining])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

type Worker struct {
	database *db.Database
	homeDir  string
	invoker  Invoker
	mcpURL   string
}

func NewWorker(database *db.Database, homeDir string, invoker Invoker) *Worker {
	return &Worker{database: database, homeDir: homeDir, invoker: invoker}
}

func (w *Worker) WithMCPURL(mcpURL string) *Worker {
	if w != nil {
		w.mcpURL = mcpURL
	}
	return w
}

// Schedule records the newest stale factual revision. It is safe to call for
// every ingest, including duplicate native-agent events.
func Schedule(database *db.Database, homeDir, projectID string, state *db.KnowledgeState) error {
	if database == nil || projectID == "" || state == nil || state.Status == db.KnowledgeCurrent {
		return nil
	}
	config, err := LoadConfig(homeDir)
	if err != nil {
		// A malformed optional config must never block factual checkpoints.
		config = DefaultConfig()
	}
	return database.EnqueueKnowledgeAuthoring(projectID, state.FactsRevision, time.Now().Add(time.Duration(config.DebounceSeconds)*time.Second))
}

// Start recovers abandoned leases and continues until context cancellation.
func (w *Worker) Start(ctx context.Context) {
	if w == nil || w.database == nil {
		return
	}
	if err := w.database.RecoverKnowledgeAuthoringJobs(); err != nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) {
	config, err := LoadConfig(w.homeDir)
	if err != nil || !config.Enabled {
		return
	}
	job, err := w.database.ClaimNextKnowledgeAuthoringJob(time.Now())
	if err != nil || job == nil {
		return
	}
	invoker := w.invoker
	if invoker == nil {
		invoker = CLIInvoker{
			MCPURL:     valueOr(w.mcpURL, "http://localhost:8765/mcp"),
			RuntimeDir: filepath.Join(w.homeDir, ".handshake", "runtime"),
		}
	}
	w.runJob(ctx, config, job, invoker)
}

func (w *Worker) runJob(parent context.Context, config Config, job *db.KnowledgeAuthoringJob, invoker Invoker) {
	state, err := w.database.GetKnowledgeState(job.ProjectID)
	if err == nil && state.Status == db.KnowledgeCurrent && state.FactsRevision >= job.TargetRevision {
		_ = w.database.CompleteKnowledgeAuthoringJob(job.ProjectID)
		return
	}
	if err != nil {
		w.finishFailure(config, job, err)
		return
	}
	root, err := w.database.GetKnowledgeProjectRoot(job.ProjectID)
	if err != nil {
		w.finishFailure(config, job, err)
		return
	}
	bundleRoot := filepath.Join(w.homeDir, ".handshake", "knowledge", job.ProjectID)
	ctx, cancel := context.WithTimeout(parent, time.Duration(config.TimeoutSeconds)*time.Second)
	err = invoker.Invoke(ctx, config.Runner, root, AuthoringPrompt(job.ProjectID, state.FactsRevision, bundleRoot))
	cancel()
	if err == nil {
		state, err = w.database.GetKnowledgeState(job.ProjectID)
		if err == nil && state.Status == db.KnowledgeCurrent && state.AIRevision == state.FactsRevision {
			_ = w.database.CompleteKnowledgeAuthoringJob(job.ProjectID)
			return
		}
		if err == nil {
			err = fmt.Errorf("writer exited without publishing both documents for revision %d", state.FactsRevision)
		}
	}
	w.finishFailure(config, job, err)
}

func (w *Worker) finishFailure(config Config, job *db.KnowledgeAuthoringJob, err error) {
	message := "unknown authoring failure"
	if err != nil {
		message = err.Error()
	}
	retry := job.Attempts <= config.RetryLimit
	retryAt := time.Now()
	if retry {
		retryAt = retryAt.Add(time.Duration(job.Attempts) * 30 * time.Second)
	}
	_ = w.database.FailKnowledgeAuthoringJob(job.ProjectID, message, retryAt, retry)
}
