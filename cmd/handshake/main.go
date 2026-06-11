package main

import (
	"fmt"
	"os"
	"path/filepath"

	"handshake/internal/db"
	"handshake/internal/engine"
	"handshake/internal/server"
	"handshake/plugins/opencode"
)

const (
	version    = "0.1.0"
	listenAddr = "localhost:8765"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Failed to determine home directory: %v\n", err)
		os.Exit(1)
	}
	dbPath := filepath.Join(homeDir, ".handshake", "sessions.db")

	switch os.Args[1] {
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
	default:
		fmt.Printf("Unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("Usage: handshake <command>")
	fmt.Println("Commands:")
	fmt.Println("  init             Create the database, install the OpenCode plugin, print MCP config")
	fmt.Println("  serve            Start the MCP + ingest server on " + listenAddr)
	fmt.Println("  list             List checkpointed sessions")
	fmt.Println("  restore <title>  Print the handoff brief for a session")
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
	fmt.Println("\nThen start the daemon with: handshake serve")
}

func serveCmd(homeDir, dbPath string) {
	database := openDB(dbPath)
	defer database.Close()

	srv := server.New("handshake", version, homeDir, database)
	fmt.Printf("Handshake %s listening on http://%s\n", version, listenAddr)
	fmt.Printf("  MCP endpoint:    http://%s/mcp\n", listenAddr)
	fmt.Printf("  Ingest endpoint: http://%s/ingest\n", listenAddr)
	if err := srv.Serve(listenAddr); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}

func listCmd(dbPath string) {
	database := openDB(dbPath)
	defer database.Close()

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
