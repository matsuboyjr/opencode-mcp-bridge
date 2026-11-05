// Modified to configure OpenCode API selection and workspace isolation.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/a0970/opencodemcpbridge/client"
	"github.com/a0970/opencodemcpbridge/config"
	"github.com/a0970/opencodemcpbridge/handlers"
	"github.com/a0970/opencodemcpbridge/mcpbridge"
	"github.com/a0970/opencodemcpbridge/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	httpClient := &http.Client{Timeout: cfg.RequestTimeout}
	opencode := client.NewWithOptions(cfg.OpenCodeURL, cfg.Username, cfg.Password, httpClient, client.Options{APIVersion: cfg.APIVersion, Directory: cfg.Directory})
	mcpServer, mcpHandler := mcpbridge.New(opencode)
	legacySSEHandler := mcpbridge.NewLegacySSEHandler(mcpServer)
	e := server.NewWithOptions(handlers.New(opencode), server.Options{AuthToken: cfg.BridgeAPIKey}, mcpHandler, legacySSEHandler)
	if cfg.BridgeAPIKey == "" {
		log.Print("bridge authentication is disabled; set OPENCODE_MCP_BRIDGE_API_KEY to enable Bearer authentication")
	}

	go func() {
		log.Printf("opencode MCP bridge listening on %s (OpenCode: %s)", cfg.ListenAddress, cfg.OpenCodeURL)
		if err := e.Start(cfg.ListenAddress); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
