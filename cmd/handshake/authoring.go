package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
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
	fmt.Println("Project Knowledge")
	fmt.Println("-----------------")
	fmt.Println("Keep the project brief, repository map, and handoff documents current.")
	fmt.Println("An active agent writes first. A fallback writer is used only when those documents remain stale.")
	if !confirmYN("Enable automatic writing?", false) {
		fmt.Println("Project knowledge will remain manual. Factual checkpoints still work.")
		return
	}

	fmt.Println()
	fmt.Println("Available fallback writers:")
	detected := authoring.DetectRunners()
	var available []authoring.DetectedRunner
	for _, candidate := range detected {
		if candidate.Err == nil {
			available = append(available, candidate)
			fmt.Printf("  * %s: %s\n", candidate.Runner, candidate.Path)
		} else {
			fmt.Printf("  - %s: not found\n", candidate.Runner)
		}
	}
	if len(available) == 0 {
		fmt.Println("No supported authoring CLI was found. Install Claude, Codex, OpenCode, or Hermes, then run this command again.")
		return
	}

	recommended := recommendedAuthoringRunner(available)
	runner := chooseAuthoringRunner(available, recommended)
	fmt.Println()
	fmt.Println("Automatic Project Knowledge")
	fmt.Printf("  Fallback writer: %s\n", runner)
	fmt.Println("  It runs only when the active agent has not refreshed the documents.")
	fmt.Println("  No model is run while this setting is being configured.")
	if !confirmYN("Enable automatic writing with this fallback?", false) {
		fmt.Println("Project knowledge remains manual. Factual checkpoints still work.")
		return
	}
	authorSetCmd(homeDir, runner)
}

func recommendedAuthoringRunner(available []authoring.DetectedRunner) authoring.Runner {
	for _, candidate := range available {
		if candidate.Runner == authoring.RunnerClaude {
			return candidate.Runner
		}
	}
	return available[0].Runner
}

func chooseAuthoringRunner(available []authoring.DetectedRunner, recommended authoring.Runner) authoring.Runner {
	defaultChoice := 1
	fmt.Println()
	fmt.Println("Choose a fallback writer:")
	for index, candidate := range available {
		label := ""
		if candidate.Runner == recommended {
			defaultChoice = index + 1
			label = " (recommended)"
		}
		fmt.Printf("  %d. %s%s\n", index+1, candidate.Runner, label)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Printf("Choose a fallback writer [%d]: ", defaultChoice)
		if !scanner.Scan() {
			return recommended
		}
		if runner, ok := parseAuthoringRunnerSelection(scanner.Text(), available, recommended); ok {
			return runner
		}
		fmt.Printf("Enter a number from 1 to %d.\n", len(available))
	}
}

func parseAuthoringRunnerSelection(input string, available []authoring.DetectedRunner, fallback authoring.Runner) (authoring.Runner, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return fallback, true
	}
	selection, err := strconv.Atoi(input)
	if err != nil || selection < 1 || selection > len(available) {
		return "", false
	}
	return available[selection-1].Runner, true
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
	fmt.Printf("Automatic project knowledge enabled with %s as the fallback writer.\n", runner)
	fmt.Println("The active agent writes first; the fallback writer runs only if documents remain stale.")
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
	fmt.Println("Automatic project knowledge disabled. Factual checkpoints continue to be recorded.")
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
	fmt.Printf("Automatic project knowledge: %s\n", status)
	runner := string(config.Runner)
	if runner == "" {
		runner = "not selected"
	}
	fmt.Printf("Fallback writer: %s\n", runner)
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
