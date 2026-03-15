// ABOUTME: CLI subcommand that returns token usage data for a
// ABOUTME: session, syncing on-demand if no server is running.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/wesm/agentsview/internal/config"
	"github.com/wesm/agentsview/internal/db"
	"github.com/wesm/agentsview/internal/parser"
	"github.com/wesm/agentsview/internal/server"
	"github.com/wesm/agentsview/internal/sync"
)

// tokenUseOutput is the JSON structure written to stdout.
// This format is experimental and may change.
type tokenUseOutput struct {
	SessionID         string `json:"session_id"`
	Agent             string `json:"agent"`
	Project           string `json:"project"`
	TotalOutputTokens int    `json:"total_output_tokens"`
	PeakContextTokens int    `json:"peak_context_tokens"`
	ServerRunning     bool   `json:"server_running"`
}

func runTokenUse(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr,
			"usage: agentsview token-use <session-id>")
		os.Exit(1)
	}
	sessionID := args[0]

	appCfg, err := config.LoadMinimal()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"error: loading config: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(appCfg.DataDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr,
			"error: creating data dir: %v\n", err)
		os.Exit(1)
	}

	serverRunning := server.FindRunningServer(
		appCfg.DataDir,
	) != nil

	database, err := db.Open(appCfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"error: opening database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	if appCfg.CursorSecret != "" {
		secret, decErr := base64.StdEncoding.DecodeString(
			appCfg.CursorSecret,
		)
		if decErr != nil {
			fmt.Fprintf(os.Stderr,
				"error: invalid cursor secret: %v\n", decErr)
			os.Exit(1)
		}
		database.SetCursorSecret(secret)
	}

	// If no server is keeping the DB in sync, do an on-demand
	// sync for this session so the data is fresh.
	if !serverRunning {
		engine := sync.NewEngine(database, sync.EngineConfig{
			AgentDirs: appCfg.AgentDirs,
			Machine:   "local",
		})
		if syncErr := engine.SyncSingleSession(
			sessionID,
		); syncErr != nil {
			// Not fatal: session may already be in the DB
			// from a previous sync, or may not exist at all.
			fmt.Fprintf(os.Stderr,
				"warning: sync failed: %v\n", syncErr)
		}
	}

	sess, err := database.GetSession(
		context.Background(), sessionID,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"error: querying session: %v\n", err)
		os.Exit(1)
	}
	if sess == nil {
		fmt.Fprintf(os.Stderr,
			"error: session not found: %s\n", sessionID)
		os.Exit(1)
	}

	agent := sess.Agent
	if agent == "" {
		if def, ok := parser.AgentByPrefix(sessionID); ok {
			agent = string(def.Type)
		}
	}

	out := tokenUseOutput{
		SessionID:         sess.ID,
		Agent:             agent,
		Project:           sess.Project,
		TotalOutputTokens: sess.TotalOutputTokens,
		PeakContextTokens: sess.PeakContextTokens,
		ServerRunning:     serverRunning,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr,
			"error: encoding output: %v\n", err)
		os.Exit(1)
	}
}
