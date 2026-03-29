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

const deployReclaimCancelReason = "superseded by newer deploy while draining"

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
		runner.NewRunner(cfg.RunnerMode, cfg.RunnerImageForRuntime(cfg.AgentRuntime), cfg.GitHubToken, cfg.RunnerSecurity),
		ghapi.NewAPIClient(cfg.GitHubToken),
		credentials.NewBroker(store, allocStrategy, cipher, cfg.CredentialLeaseTTL),
		cipher,
		fmt.Sprintf("%s-%d-%d", cfg.Slot, os.Getpid(), time.Now().UTC().UnixNano()),
	)
	if err := s.BootstrapAuth(); err != nil {
		log.Fatalf("auth bootstrap: %v", err)
	}
	if err := s.StartRunResultReporter(); err != nil {
		log.Fatalf("run result reporter: %v", err)
	}
	defer func() {
		if err := s.StopRunResultReporter(); err != nil {
			log.Printf("run result reporter shutdown warning: %v", err)
		}
	}()
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

	log.Printf("rascald listening on %s (runner=%s runtime=%s docker_security=%s)", cfg.ListenAddr, cfg.RunnerMode, cfg.AgentRuntime, cfg.RunnerSecurity.Summary())
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpServer.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGUSR1, syscall.SIGUSR2)
	defer signal.Stop(sigCh)

	var deployDrainDone <-chan struct{}
	for {
		select {
		case err := <-serverErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("server stopped: %v", err)
			}
			return
		case <-deployDrainDone:
			log.Printf("deploy drain complete; shutting down slot")
			s.StopRunSupervisors()
			return
		case sig := <-sigCh:
			switch sig {
			case syscall.SIGUSR1:
				if deployDrainDone == nil {
					log.Printf("deploy drain signal received; stopping http listeners and waiting for active runs to finish")
					deployDrainDone = beginDeployDrain(httpServer, s)
				}
			case syscall.SIGUSR2:
				log.Printf("deploy reclaim signal received; canceling active runs before shutdown")
				reclaimForDeploy(httpServer, s, 15*time.Second)
				return
			case os.Interrupt, syscall.SIGTERM:
				log.Printf("shutdown signal received; entering drain mode")
				genericShutdown(httpServer, s, 10*time.Second)
				return
			}
		}
	}
}

func beginDeployDrain(httpServer *http.Server, s *orchestrator.Server) <-chan struct{} {
	s.BeginDrain()
	shutdownHTTPServer(httpServer, 15*time.Second)

	done := make(chan struct{})
	go func() {
		waitForActiveRunsToFinish(s)
		close(done)
	}()
	return done
}

func reclaimForDeploy(httpServer *http.Server, s *orchestrator.Server, timeout time.Duration) {
	s.BeginDrain()
	shutdownHTTPServer(httpServer, 15*time.Second)
	s.CancelActiveRunsWithReason(deployReclaimCancelReason, state.RunStatusReasonDeployReclaimed)
	if err := s.WaitForNoActiveRuns(timeout); err != nil {
		log.Printf("deploy reclaim exiting with active detached runs still executing: %v", err)
	}
	s.StopRunSupervisors()
}

func genericShutdown(httpServer *http.Server, s *orchestrator.Server, timeout time.Duration) {
	s.BeginDrain()
	shutdownHTTPServer(httpServer, 15*time.Second)
	s.StopRunSupervisors()
	if err := s.WaitForNoActiveRuns(timeout); err != nil {
		log.Printf("shutdown exiting with active detached runs still executing: %v", err)
	}
}

func shutdownHTTPServer(httpServer *http.Server, timeout time.Duration) {
	if httpServer == nil {
		return
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("http shutdown warning: %v", err)
	}
}

func waitForActiveRunsToFinish(s *orchestrator.Server) {
	for {
		if s.ActiveRunCount() == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
