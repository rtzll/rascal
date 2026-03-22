package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/credentials"
	credentialstrategies "github.com/rtzll/rascal/internal/credentials/strategies"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/orchestrator"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/state"
)

func main() {
	cfg, err := config.LoadServerConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := cfg.Ensure(); err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := state.New(cfg.StatePath, cfg.MaxRuns)
	if err != nil {
		log.Fatalf("state: %v", err)
	}

	allocStrategy, err := credentialstrategies.ByName(cfg.CredentialStrategy)
	if err != nil {
		log.Fatalf("credential strategy: %v", err)
	}
	cipher, err := credentials.NewAESCipher(cfg.CredentialEncryptionKey)
	if err != nil {
		log.Fatalf("credential cipher: %v", err)
	}

	s := orchestrator.NewServer(
		cfg,
		store,
		runner.NewRunner(cfg.RunnerMode, cfg.RunnerImageForRuntime(cfg.AgentRuntime), cfg.GitHubToken),
		ghapi.NewAPIClient(cfg.GitHubToken),
		credentials.NewBroker(store, allocStrategy, cipher, cfg.CredentialLeaseTTL),
		cipher,
		fmt.Sprintf("%s-%d-%d", cfg.Slot, os.Getpid(), time.Now().UTC().UnixNano()),
	)
	if err := s.BootstrapAuth(); err != nil {
		log.Fatalf("auth bootstrap: %v", err)
	}
	s.RecoverQueuedCancels()
	s.RecoverRunningRuns()
	s.ScheduleRuns("")

	mux := http.NewServeMux()
	s.Mount(mux)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           orchestrator.WithRequestID(orchestrator.LogRequests(mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("rascald listening on %s (runner=%s backend=%s)", cfg.ListenAddr, cfg.RunnerMode, cfg.AgentRuntime)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpServer.ListenAndServe()
	}()

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server stopped: %v", err)
		}
		return
	case <-sigCtx.Done():
	}

	log.Printf("shutdown signal received; entering drain mode")
	s.BeginDrain()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("http shutdown warning: %v", err)
	}

	s.StopRunSupervisors()
	if err := s.WaitForNoActiveRuns(10 * time.Second); err != nil {
		log.Printf("shutdown exiting with active detached runs still executing: %v", err)
	}
}
