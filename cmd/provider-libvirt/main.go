/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/projectbeskar/virtrigaud/internal/providers/libvirt"
	"github.com/projectbeskar/virtrigaud/internal/version"
	providerv1 "github.com/projectbeskar/virtrigaud/proto/rpc/provider/v1"
)

func main() {
	// Handle --version flag before any other flag parsing
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("virtrigaud-provider-libvirt %s\n", version.String())
		os.Exit(0)
	}

	var port int
	var healthPort int
	flag.IntVar(&port, "port", 9443, "gRPC server port")
	flag.IntVar(&healthPort, "health-port", 8080, "Health check port")
	flag.Parse()

	// Create logger with configurable format
	var handler slog.Handler
	logFormat := os.Getenv("LOG_FORMAT")
	if logFormat == "json" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: getLogLevel(),
		})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: getLogLevel(),
		})
	}
	logger := slog.New(handler)

	// Create context that listens for the interrupt signal from the OS
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Create gRPC server
	server := grpc.NewServer()

	// Create Libvirt provider with SDK pattern (reads config from environment)
	providerImpl := libvirt.New()
	provider := libvirt.NewServer(providerImpl)

	// Register the provider service
	providerv1.RegisterProviderServer(server, provider)

	// Register health service
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Start gRPC server
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		logger.Error("Failed to listen", "error", err)
		os.Exit(1)
	}

	logger.Info("Starting Libvirt provider server",
		"version", version.String(),
		"log_level", getLogLevel().String(),
		"log_format", logFormat,
		"port", port,
		"health_port", healthPort,
		"capabilities", []string{
			"core", "snapshots", "linked-clones",
			"online-reconfigure", "qemu-guest-agent",
		},
		"supported_platforms", []string{"kvm", "qemu", "libvirt"},
	)

	// Create HTTP health server
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	healthMux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", healthPort),
		Handler: healthMux,
	}

	logger.Info("Starting HTTP health server", "port", healthPort)

	// Start gRPC server in a goroutine
	go func() {
		if err := server.Serve(lis); err != nil {
			logger.Error("Failed to serve gRPC", "error", err)
			os.Exit(1)
		}
	}()

	// Start HTTP health server in a goroutine
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Failed to serve HTTP health server", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	<-ctx.Done()

	logger.Info("Shutting down gRPC server...")
	server.GracefulStop()

	logger.Info("Shutting down HTTP health server...")
	_ = httpServer.Shutdown(context.Background())
}

// getLogLevel returns the log level from LOG_LEVEL environment variable.
// Supported values: debug, warn, error, info (default)
func getLogLevel() slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
