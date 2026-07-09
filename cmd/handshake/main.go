package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"

	"handshake/internal/adapters"
	"handshake/internal/db"
	"handshake/internal/engine"
	"handshake/internal/git"
	"handshake/internal/server"
	"handshake/internal/timeline"
	"handshake/internal/tui"
	"handshake/plugins/opencode"
)

// version is stamped by GoReleaser at build time (-X main.version=...).
var version = "0.1.0-dev"

const listenAddr = "localhost:8765"

func main() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Failed to determine home directory: %v\n", err)
		os.Exit(1)
	}
	dbPath := filepath.Join(homeDir, ".handshake", "sessions.db")

	// No arguments — run guided setup (triggered by brew post-install hook
	// and friendly for first-time users who just type "handshake").
	if len(os.Args) < 2 {
		setupCmd(homeDir, dbPath)
		return
	}

	switch os.Args[1] {
	case "setup":
		setupCmd(homeDir, dbPath)
	case "init":
		initCmd(homeDir, dbPath)
	case "serve":
		serveCmd(homeDir, dbPath)
	case "list":
		listCmd(homeDir, dbPath)
	case "pull":
		agent := ""
		if len(os.Args) > 2 {
			agent = os.Args[2]
		}
		pullCmd(homeDir, dbPath, agent)
	case "browse", "ui":
		browseCmd(homeDir, dbPath)
	case "restore":
		if len(os.Args) < 3 {
			fmt.Println("Usage: handshake restore <title>")
			os.Exit(1)
		}
		restoreCmd(dbPath, os.Args[2])
	case "timeline":
		if len(os.Args) < 3 {
			fmt.Println("Usage: handshake timeline <title>")
			os.Exit(1)
		}
		timelineCmd(dbPath, os.Args[2])
	case "install-service":
		installServiceCmd(homeDir)
	case "uninstall-service":
		uninstallServiceCmd(homeDir)
	case "uninstall":
		uninstallCmd(homeDir)
	case "version", "--version", "-v":
		fmt.Println("handshake " + version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Printf("Unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("Usage: handshake <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  setup              Guided setup — registers with agents, installs service (default)")
	fmt.Println("  init               Non-interactive setup — create database and register with agents")
	fmt.Println("  serve              Start the MCP + ingest server (default: " + listenAddr + ")")
	fmt.Println("  browse             Interactive session browser (TUI)")
	fmt.Println("  list               List checkpointed sessions (opens browser on a terminal)")
	fmt.Println("  pull [agent]       Import sessions from agents' native storage")
	fmt.Println("                     (agents: " + strings.Join(adapters.PullableAgents, ", ") + "; default all)")
	fmt.Println("  restore <title>    Print the handoff brief for a session")
	fmt.Println("  timeline <title>   Print a session's activity timeline (prompts, tools, commits)")
	fmt.Println("  install-service    Start the daemon on login (launchd/systemd)")
	fmt.Println("  uninstall-service  Remove the login service")
	fmt.Println("  uninstall          Remove Handshake from all agents and clean up")
	fmt.Println("  version            Print the version")
	fmt.Println("  help               Show this message")
	fmt.Println()
	fmt.Println("Environment variables:")
	fmt.Println("  HANDSHAKE_ADDR     Daemon listen address (e.g. localhost:8766)")
	fmt.Println("  HANDSHAKE_URL      MCP endpoint URL registered with agents")
	fmt.Println("                     (e.g. http://localhost:8766/mcp)")
	fmt.Println("                     Set both when another tool already uses port 8765.")
}

// findFreePort returns addr if the port is available, otherwise increments the
// port number until a free one is found (up to +100). Returns addr unchanged
// on any parse error.
func findFreePort(addr string) string {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return addr
	}
	for p := port; p < port+100; p++ {
		candidate := net.JoinHostPort(host, strconv.Itoa(p))
		ln, err := net.Listen("tcp", candidate)
		if err == nil {
			ln.Close()
			return candidate
		}
	}
	return addr
}

func openDB(dbPath string) *db.Database {
	database, err := db.New(dbPath)
	if err != nil {
		fmt.Printf("Failed to open database: %v\n", err)
		os.Exit(1)
	}
	return database
}

func initCmd(homeDir, dbPath string) {
	database := openDB(dbPath)
	database.Close()
	fmt.Printf("✓ Database ready at %s\n", dbPath)

	pluginPath := filepath.Join(homeDir, ".config", "opencode", "plugins", "handshake.js")
	if err := os.MkdirAll(filepath.Dir(pluginPath), 0755); err != nil {
		fmt.Printf("✗ Could not create OpenCode plugin directory: %v\n", err)
	} else if err := os.WriteFile(pluginPath, opencode.PluginJS, 0644); err != nil {
		fmt.Printf("✗ Could not install OpenCode plugin: %v\n", err)
	} else {
		fmt.Printf("✓ OpenCode plugin installed at %s\n", pluginPath)
	}

	fmt.Println("\nRegistering Handshake as an MCP server with each agent:")
	registerAgents(homeDir)
	registerClaudeCodeHooks(homeDir)
	registerHermesPlugin(homeDir)
	registerCodexHooks(homeDir)
	fmt.Println("\nThen start the daemon with: handshake serve")
}

func serveCmd(homeDir, dbPath string) {
	addr := listenAddr
	if env := os.Getenv("HANDSHAKE_ADDR"); env != "" {
		addr = env
	}

	database := openDB(dbPath)
	defer database.Close()

	srv := server.New("handshake", version, homeDir, database)
	fmt.Printf("Handshake %s listening on http://%s\n", version, addr)
	fmt.Printf("  MCP endpoint:    http://%s/mcp\n", addr)
	fmt.Printf("  Ingest endpoint: http://%s/ingest\n", addr)

	// Catch-up sync: sweep in sessions that ended while the daemon was down
	// or whose agent hooks never fired. Runs in the background so the port
	// binds immediately.
	go func() {
		imported, updated := 0, 0
		for _, res := range adapters.SyncAll(database, homeDir) {
			imported += res.Imported
			updated += res.Updated
		}
		if imported+updated > 0 {
			fmt.Printf("  Catch-up sync:   %d session(s) imported, %d updated\n", imported, updated)
		}
	}()
	if err := srv.Serve(addr); err != nil {
		if strings.Contains(err.Error(), "address already in use") {
			fmt.Printf("Port conflict: %s is already in use.\n", addr)
			fmt.Println("Another process (possibly another MCP tool) is listening on this port.")
			fmt.Println()
			fmt.Println("Fix: pick a free port and re-run setup so the MCP URL is updated:")
			fmt.Println()
			fmt.Printf("  HANDSHAKE_ADDR=localhost:8766 handshake serve\n")
			fmt.Printf("  HANDSHAKE_URL=http://localhost:8766/mcp handshake setup\n")
		} else {
			fmt.Printf("Server error: %v\n", err)
		}
		os.Exit(1)
	}
}

// pullCmd imports sessions from agents' native local storage. agent narrows
// the pull to one agent; empty pulls all of them.
func pullCmd(homeDir, dbPath, agent string) {
	if agent != "" && !contains(adapters.PullableAgents, agent) {
		fmt.Printf("Unknown agent %q — pullable agents: %s\n", agent, strings.Join(adapters.PullableAgents, ", "))
		os.Exit(1)
	}

	database := openDB(dbPath)
	defer database.Close()

	var results []adapters.SyncResult
	if agent != "" {
		results = []adapters.SyncResult{adapters.SyncAgent(database, homeDir, agent)}
	} else {
		results = adapters.SyncAll(database, homeDir)
	}

	imported, updated := 0, 0
	for _, res := range results {
		switch {
		case res.Err != nil:
			fmt.Printf("✗ %-12s %v\n", res.Agent, res.Err)
		case res.Scanned == 0:
			fmt.Printf("- %-12s no local sessions found\n", res.Agent)
		default:
			line := fmt.Sprintf("✓ %-12s %d scanned", res.Agent, res.Scanned)
			if res.Imported > 0 {
				line += fmt.Sprintf(" · %d new", res.Imported)
			}
			if res.Updated > 0 {
				line += fmt.Sprintf(" · %d updated", res.Updated)
			}
			if res.Unchanged > 0 {
				line += fmt.Sprintf(" · %d unchanged", res.Unchanged)
			}
			if res.Failed > 0 {
				line += fmt.Sprintf(" · %d failed", res.Failed)
			}
			fmt.Println(line)
			imported += res.Imported
			updated += res.Updated
		}
	}

	if imported+updated == 0 {
		fmt.Println("\nEverything already up to date.")
	} else {
		fmt.Printf("\n%d session(s) imported, %d updated. Browse them with: handshake browse\n", imported, updated)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func listCmd(homeDir, dbPath string) {
	// On a terminal, open the interactive browser; keep plain text output
	// when piped so scripts and agents can still parse it.
	if isatty.IsTerminal(os.Stdout.Fd()) {
		browseCmd(homeDir, dbPath)
		return
	}

	database := openDB(dbPath)
	defer database.Close()
	// Auto-sync Codex sessions before listing
	adapters.IngestCodexSessions(database, homeDir)

	sessions, err := database.ListSessions("")
	if err != nil {
		fmt.Printf("Failed to list sessions: %v\n", err)
		os.Exit(1)
	}
	if len(sessions) == 0 {
		fmt.Println("No sessions found")
		return
	}

	fmt.Println("Recent sessions:")
	for _, session := range sessions {
		fmt.Printf("- %s (%s)\n", session.Title, session.Agent)
	}
}

// browseCmd runs the interactive session browser. If the user confirms a
// restore inside the TUI, the packet and brief are printed after the
// terminal is restored — same output shape as `handshake restore`.
func browseCmd(homeDir, dbPath string) {
	database := openDB(dbPath)
	defer database.Close()

	session, err := tui.Run(database, homeDir)
	if err != nil {
		fmt.Printf("Browser error: %v\n", err)
		os.Exit(1)
	}
	if session == nil {
		return
	}

	if session.GitState != "" {
		var checkpoint git.State
		if err := json.Unmarshal([]byte(session.GitState), &checkpoint); err == nil {
			workingDir := session.WorkingDir
			if workingDir == "" {
				workingDir = homeDir
			}
			if packet := git.BuildRestorePacket(session.Title, session.Agent, session.UpdatedAt, workingDir, &checkpoint, session.Summary, session.Decisions); packet != "" {
				fmt.Println(packet)
				fmt.Println()
			}
		}
	}

	brief, err := engine.NewBriefGenerator(database).GenerateBrief(session.ID)
	if err != nil {
		fmt.Printf("Failed to generate brief: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(brief.Brief)
}

func timelineCmd(dbPath, title string) {
	database := openDB(dbPath)
	defer database.Close()

	session := findSessionCLI(database, title)
	chapters, err := timeline.Build(database, session)
	if err != nil {
		fmt.Printf("Failed to build timeline: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(timeline.Render(session, chapters))
}

func restoreCmd(dbPath, title string) {
	database := openDB(dbPath)
	defer database.Close()

	session := findSessionCLI(database, title)

	// If the session has stored git state, show the restore packet and prompt.
	if session.GitState != "" {
		var checkpoint git.State
		if err := json.Unmarshal([]byte(session.GitState), &checkpoint); err == nil {
			workingDir := session.WorkingDir
			if workingDir == "" {
				workingDir, _ = os.UserHomeDir()
			}
			packet := git.BuildRestorePacket(session.Title, session.Agent, session.UpdatedAt, workingDir, &checkpoint, session.Summary, session.Decisions)
			if packet != "" {
				fmt.Println(packet)
				fmt.Print("\nInject this context? [Y/n] ")
				scanner := bufio.NewScanner(os.Stdin)
				scanner.Scan()
				answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
				if answer == "n" || answer == "no" {
					fmt.Println("Restore cancelled.")
					return
				}
				fmt.Println()
			}
		}
	}

	brief, err := engine.NewBriefGenerator(database).GenerateBrief(session.ID)
	if err != nil {
		fmt.Printf("Failed to generate brief: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(brief.Brief)
}

// findSessionCLI fuzzy-matches a title against checkpointed sessions:
// exact match first, then substring. Exits if no match found.
func findSessionCLI(database *db.Database, title string) *db.Session {
	sessions, err := database.ListSessions("")
	if err != nil {
		fmt.Printf("Failed to list sessions: %v\n", err)
		os.Exit(1)
	}

	q := strings.ToLower(strings.TrimSpace(title))
	var submatches []*db.Session
	for _, s := range sessions {
		t := strings.ToLower(s.Title)
		if t == q {
			return s // exact match wins immediately
		}
		if strings.Contains(t, q) {
			submatches = append(submatches, s)
		}
	}
	if len(submatches) > 0 {
		if len(submatches) > 1 {
			fmt.Printf("Multiple sessions match %q — using most recent: %s\n\n", title, submatches[0].Title)
		}
		return submatches[0]
	}
	fmt.Printf("Session not found: %s\n", title)
	os.Exit(1)
	return nil
}
