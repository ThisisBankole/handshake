package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"handshake/internal/authoring"
)

func knowledgeCmd(homeDir, dbPath string, args []string) {
	if len(args) == 0 || args[0] != "author" {
		fmt.Println("Usage: handshake knowledge author <setup|show|set|off|test>")
		return
	}
	if len(args) < 2 {
		fmt.Println("Usage: handshake knowledge author <setup|show|set|off|test>")
		return
	}
	switch args[1] {
	case "setup":
		authorSetupCmd(homeDir)
	case "show":
		authorShowCmd(homeDir, dbPath)
	case "set":
		if len(args) != 3 {
			fmt.Println("Usage: handshake knowledge author set <claude|codex|opencode|hermes>")
			return
		}
		authorSetCmd(homeDir, authoring.Runner(args[2]))
	case "off":
		authorOffCmd(homeDir)
	case "test":
		runner := authoring.Runner("")
		if len(args) == 3 {
			runner = authoring.Runner(args[2])
		}
		authorTestCmd(homeDir, runner)
	default:
		fmt.Println("Usage: handshake knowledge author <setup|show|set|off|test>")
	}
}

func authorSetupCmd(homeDir string) {
	detected := authoring.DetectRunners()
	var available []authoring.DetectedRunner
	for _, candidate := range detected {
		if candidate.Err == nil {
			available = append(available, candidate)
			fmt.Printf("  ✓ %s: %s\n", candidate.Runner, candidate.Path)
		} else {
			fmt.Printf("  - %s: not found\n", candidate.Runner)
		}
	}
	if len(available) == 0 {
		fmt.Println("No supported authoring CLI was found. Install Claude, Codex, OpenCode, or Hermes, then run this command again.")
		return
	}
	recommended := available[0].Runner
	for _, candidate := range available {
		if candidate.Runner == authoring.RunnerClaude {
			recommended = candidate.Runner
			break
		}
	}
	fmt.Printf("\nRecommended writer: %s\n", recommended)
	fmt.Println("It is a safety net: Handshake first gives an active agent time to publish documents.")
	fmt.Println("It may consume your model quota when that fallback is needed.")
	if !confirmYN("Enable this background writer?", false) {
		fmt.Println("Background authoring remains off. Factual checkpoints still work.")
		return
	}
	authorSetCmd(homeDir, recommended)
}

func authorSetCmd(homeDir string, runner authoring.Runner) {
	if !authoring.IsSupported(runner) {
		fmt.Printf("Unsupported writer %q. Choose: claude, codex, opencode, hermes\n", runner)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := authoring.ProbeRunner(ctx, runner)
	cancel()
	if err != nil {
		fmt.Printf("Writer check failed: %v\n", err)
		return
	}
	config, err := authoring.LoadConfig(homeDir)
	if err != nil {
		fmt.Printf("Could not read authoring configuration: %v\n", err)
		return
	}
	config.Enabled = true
	config.Runner = runner
	if err := authoring.SaveConfig(homeDir, config); err != nil {
		fmt.Printf("Could not save authoring configuration: %v\n", err)
		return
	}
	fmt.Printf("Background writer enabled: %s\n", runner)
	fmt.Println("New factual checkpoints give an active agent time to author first; this writer runs only if documents remain stale.")
}

func authorOffCmd(homeDir string) {
	config, err := authoring.LoadConfig(homeDir)
	if err != nil {
		fmt.Printf("Could not read authoring configuration: %v\n", err)
		return
	}
	config.Enabled = false
	if err := authoring.SaveConfig(homeDir, config); err != nil {
		fmt.Printf("Could not save authoring configuration: %v\n", err)
		return
	}
	fmt.Println("Background writer disabled. Factual checkpoints continue to be recorded.")
}

func authorTestCmd(homeDir string, runner authoring.Runner) {
	if runner == "" {
		config, err := authoring.LoadConfig(homeDir)
		if err != nil {
			fmt.Printf("Could not read authoring configuration: %v\n", err)
			return
		}
		runner = config.Runner
	}
	if runner == "" {
		fmt.Println("No writer selected. Run: handshake knowledge author setup")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err := authoring.ProbeRunner(ctx, runner)
	cancel()
	if err != nil {
		fmt.Printf("Writer check failed: %v\n", err)
		return
	}
	fmt.Printf("%s is installed and accepts non-interactive CLI checks.\n", runner)
}

func authorShowCmd(homeDir, dbPath string) {
	config, err := authoring.LoadConfig(homeDir)
	if err != nil {
		fmt.Printf("Could not read authoring configuration: %v\n", err)
		return
	}
	status := "off"
	if config.Enabled {
		status = "on"
	}
	fmt.Printf("Background writer: %s\n", status)
	runner := string(config.Runner)
	if runner == "" {
		runner = "not selected"
	}
	fmt.Printf("Runner: %s\n", runner)
	fmt.Printf("Debounce: %ds; timeout: %ds; retries: %d\n", config.DebounceSeconds, config.TimeoutSeconds, config.RetryLimit)
	database := openDB(dbPath)
	defer database.Close()
	jobs, err := database.ListKnowledgeAuthoringJobs()
	if err != nil {
		fmt.Printf("Could not list authoring jobs: %v\n", err)
		return
	}
	if len(jobs) == 0 {
		fmt.Println("Jobs: none")
		return
	}
	fmt.Println("Jobs:")
	for _, job := range jobs {
		line := fmt.Sprintf("  %s revision %d: %s (attempt %d)", job.ProjectID, job.TargetRevision, job.State, job.Attempts)
		if strings.TrimSpace(job.LastError) != "" {
			line += ": " + job.LastError
		}
		fmt.Println(line)
	}
}
