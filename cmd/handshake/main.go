package main

import (
	"fmt"
	"os"
	"path/filepath"

	"handshake/internal/db"
	"handshake/internal/engine"
	"handshake/internal/server"
	"handshake/plugins/opencode"
	"handshake/internal/adapters"
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
		listCmd(dbPath)
	case "restore":
		if len(os.Args) < 3 {
			fmt.Println("Usage: handshake restore <title>")
			os.Exit(1)
		}
		restoreCmd(dbPath, os.Args[2])
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
	fmt.Println("  serve              Start the MCP + ingest server on " + listenAddr)
	fmt.Println("  list               List checkpointed sessions")
	fmt.Println("  restore <title>    Print the handoff brief for a session")
	fmt.Println("  install-service    Start the daemon on login (launchd/systemd)")
	fmt.Println("  uninstall-service  Remove the login service")
	fmt.Println("  uninstall          Remove Handshake from all agents and clean up")
	fmt.Println("  version            Print the version")
	fmt.Println("  help               Show this message")
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
	if err := srv.Serve(addr); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

func listCmd(dbPath string) {
	database := openDB(dbPath)
	defer database.Close()
	// Auto-sync Codex sessions before listing
	homeDir, _ := os.UserHomeDir()
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

func restoreCmd(dbPath, title string) {
	database := openDB(dbPath)
	defer database.Close()

	sessions, err := database.ListSessions("")
	if err != nil {
		fmt.Printf("Failed to list sessions: %v\n", err)
		os.Exit(1)
	}

	var sessionID string
	for _, session := range sessions {
		if session.Title == title {
			sessionID = session.ID
			break
		}
	}
	if sessionID == "" {
		fmt.Printf("Session not found: %s\n", title)
		os.Exit(1)
	}

	brief, err := engine.NewBriefGenerator(database).GenerateBrief(sessionID)
	if err != nil {
		fmt.Printf("Failed to generate brief: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(brief.Brief)
}
