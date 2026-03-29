package orchestrator

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/state"
)

const runResultSocketFile = "rr.sock"

func (s *Server) RunResultSocketPath() string {
	dataDir := strings.TrimSpace(s.Config.DataDir)
	sum := sha1.Sum([]byte(dataDir))
	key := fmt.Sprintf("%x", sum[:6])
	return filepath.Join("/tmp", "rascal-control", key, runResultSocketFile)
}

func (s *Server) StartRunResultReporter() error {
	if s == nil {
		return fmt.Errorf("server is required")
	}
	socketPath := s.RunResultSocketPath()
	if strings.TrimSpace(socketPath) == "" {
		return fmt.Errorf("run result socket path is required")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return fmt.Errorf("create run result socket directory: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runResultListener != nil {
		return nil
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale run result socket %s: %w", socketPath, err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on run result socket %s: %w", socketPath, err)
	}
	s.runResultListener = listener
	s.runResultWG.Add(1)
	go s.serveRunResultReports(listener)
	return nil
}

func (s *Server) StopRunResultReporter() error {
	if s == nil {
		return nil
	}
	socketPath := s.RunResultSocketPath()

	s.mu.Lock()
	listener := s.runResultListener
	s.runResultListener = nil
	s.mu.Unlock()

	if listener == nil {
		if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove run result socket %s: %w", socketPath, err)
		}
		return nil
	}
	err := listener.Close()
	s.runResultWG.Wait()
	if rmErr := os.Remove(socketPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		if err != nil {
			return fmt.Errorf("close run result listener: %w (remove socket: %v)", err, rmErr)
		}
		return fmt.Errorf("remove run result socket %s: %w", socketPath, rmErr)
	}
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("close run result listener: %w", err)
	}
	return nil
}

func (s *Server) serveRunResultReports(listener net.Listener) {
	defer s.runResultWG.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("accept run result report failed: %v", err)
			continue
		}
		s.runResultWG.Add(1)
		go func() {
			defer s.runResultWG.Done()
			s.handleRunResultReport(conn)
		}()
	}
}

func (s *Server) handleRunResultReport(conn net.Conn) {
	defer closeRunResultReportConn(conn)
	var result runner.RunResult
	if err := json.NewDecoder(conn).Decode(&result); err != nil {
		encodeRunResultResponse(conn, runner.RunResultReportResponse{OK: false, Error: fmt.Sprintf("decode request: %v", err)})
		return
	}
	if strings.TrimSpace(result.RunID) == "" {
		encodeRunResultResponse(conn, runner.RunResultReportResponse{OK: false, Error: "run id is required"})
		return
	}
	if _, err := s.Store.ReportRunExecutionResult(state.RunExecutionResult{
		RunID:         strings.TrimSpace(result.RunID),
		ExitCode:      result.ExitCode,
		ErrorText:     strings.TrimSpace(result.Error),
		PRNumber:      result.PRNumber,
		PRURL:         strings.TrimSpace(result.PRURL),
		HeadSHA:       strings.TrimSpace(result.HeadSHA),
		TaskSessionID: strings.TrimSpace(result.TaskSessionID),
	}); err != nil {
		encodeRunResultResponse(conn, runner.RunResultReportResponse{OK: false, Error: err.Error()})
		return
	}
	encodeRunResultResponse(conn, runner.RunResultReportResponse{OK: true})
}

func encodeRunResultResponse(conn net.Conn, resp runner.RunResultReportResponse) {
	if err := json.NewEncoder(conn).Encode(resp); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, os.ErrClosed) {
		log.Printf("encode run result response failed: %v", err)
	}
}

func closeRunResultReportConn(conn net.Conn) {
	if conn == nil {
		return
	}
	if err := conn.Close(); err != nil {
		return
	}
}
