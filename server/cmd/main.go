package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"qingqiu-world-server/internal/api"
	"qingqiu-world-server/internal/api/handler"
	"qingqiu-world-server/internal/config"
	"qingqiu-world-server/internal/database"
	"qingqiu-world-server/internal/dops"
	"qingqiu-world-server/internal/migration"
	"qingqiu-world-server/internal/service/energy"
	"qingqiu-world-server/internal/service/eventqueue"
	"qingqiu-world-server/internal/service/experience"
	"qingqiu-world-server/internal/service/kb"
	"qingqiu-world-server/internal/service/llm"
	"qingqiu-world-server/internal/service/memory"
	"qingqiu-world-server/internal/service/runtime"

	applogger "qingqiu-world-server/internal/logger"

	"github.com/joho/godotenv"
)

// safeMarshalSSE marshals data to JSON for SSE push, logging on failure.
// Returns the JSON string or an empty string if marshaling fails.
func safeMarshalSSE(data map[string]interface{}) string {
	bytes, err := json.Marshal(data)
	if err != nil {
		applogger.Error("Failed to marshal SSE event data", "error", err)
		return ""
	}
	return string(bytes)
}

func main() {
	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)
	envFile := filepath.Join(exeDir, ".env")
	if err := godotenv.Load(envFile); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load %s: %v\n", envFile, err)
	}
	if err := godotenv.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load .env from cwd: %v\n", err)
	}

	config.Init()
	applogger.Init()
	applogger.Info("Qingqiu World Server config initialized", "config", config.Get())

	applogger.Info("Starting Qingqiu World Server")

	database.Init()
	migration.Run()

	// Energy: initialize the global fixed timezone from <DATA_ROOT>/tz.txt.
	// Must run before any energy operation (RecoverEnergy/DeductEnergy) and
	// before agent runtimes start (which call RecoverEnergy on startup).
	if err := energy.Init(); err != nil {
		applogger.Error("energy timezone init failed, falling back to UTC", "error", err)
		// Non-fatal: energy package falls back to UTC internally.
	}

	llm.LoadCapabilityCache()

	// Initialize the embedding service once — shared by memory and experience systems.
	embSvc := getEmbeddingService()

	// Memory system: event vectorization + daily cron
	memory.Init(embSvc)
	memCtx, memCancel := context.WithCancel(context.Background())
	go memory.Start(memCtx)

	// Initialize the Agent Runtime system with SSE callbacks
	onStatusChange := func(agentConfigID, personID, sessionID int64, status int) {
		data := safeMarshalSSE(map[string]interface{}{
			"type":       "agent_status",
			"agent_id":   personID,
			"session_id": sessionID,
			"status":     status,
		})
		if data != "" {
			handler.PushSSEToSession(sessionID, data)
		}
	}
	onPushMessage := func(sessionID, messageID, personID int64, content string) {
		data := safeMarshalSSE(map[string]interface{}{
			"type":       "message",
			"message_id": messageID,
			"person_id":  personID,
			"content":    content,
		})
		if data != "" {
			handler.PushSSEToSession(sessionID, data)
		}
	}

	// Experience system: semantic retrieval for tasks + heartbeat-triggered reflection.
	experience.Init(embSvc)

	// Initialize the global event queue first, before runtimes subscribe to it
	eventqueue.Init()

	// Start all agent runtimes and recover orphaned scheduled events.
	// recoverScheduledEvents() is called inside Start() after all runtimes
	// have subscribed to the event queue.
	runtime.Start(onStatusChange, onPushMessage, handler.PushSSEToSession)

	kb.Init(kb.DefaultEmbeddingDim, 0)
	kb.RecoverProcessingDocuments()

	r := api.SetupRouter()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	addr := fmt.Sprintf(":%s", port)

	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	// Start server in a goroutine so we can listen for shutdown signals
	go func() {
		applogger.Info("Server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			applogger.Error("Server failed to start", "error", err)
			panic(err)
		}
	}()

	// Wait for interrupt signal for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	applogger.Info("Received shutdown signal", "signal", sig.String())

	// Stop all agent runtimes and wait for graceful completion.
	applogger.Info("Stopping agent runtimes...")
	runtime.Shutdown(10 * time.Second)

	// Shut down the memory system (vectorization + daily cron)
	memCancel()

	// Close all SSE connections so HTTP shutdown doesn't wait for keep-alive
	handler.ShutdownSSE()

	// Graceful HTTP shutdown — returns immediately since SSE connections are closed
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		applogger.Warn("HTTP server shutdown", "error", err)
	}

	applogger.Info("Server stopped gracefully")
}

// getEmbeddingService creates an EmbeddingService from the global embedding config.
// Returns nil if no embedding config exists.
func getEmbeddingService() *llm.EmbeddingService {
	embConfig := dops.GetEmbeddingConfig()
	if embConfig == nil {
		return nil
	}

	embSvc := llm.NewEmbeddingService(embConfig.BaseURL, embConfig.APIKey, embConfig.ModelID, kb.DefaultEmbeddingDim)
	applogger.Info("Embedding service created",
		"config_name", embConfig.Name,
		"model", embConfig.ModelID,
	)
	return embSvc
}
