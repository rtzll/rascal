package runner

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"
)

func ReportRunResult(socketPath string, result RunResult) error {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return nil
	}
	if strings.TrimSpace(result.RunID) == "" {
		return fmt.Errorf("run id is required")
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("dial run result socket %s: %w", socketPath, err)
	}
	defer closeRunResultConn(conn)
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("set run result socket deadline: %w", err)
	}
	if err := json.NewEncoder(conn).Encode(result); err != nil {
		return fmt.Errorf("encode run result report: %w", err)
	}
	var resp RunResultReportResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("decode run result response: %w", err)
	}
	if !resp.OK {
		if strings.TrimSpace(resp.Error) == "" {
			return fmt.Errorf("run result report rejected")
		}
		return fmt.Errorf("run result report rejected: %s", strings.TrimSpace(resp.Error))
	}
	return nil
}

func closeRunResultConn(conn net.Conn) {
	if conn == nil {
		return
	}
	if err := conn.Close(); err != nil {
		return
	}
}

func ReportRunResultWithRetry(socketPath string, result RunResult, attempts int, delay time.Duration) error {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return nil
	}
	if attempts <= 0 {
		attempts = 1
	}
	if delay <= 0 {
		delay = 250 * time.Millisecond
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ReportRunResult(socketPath, result); err != nil {
			lastErr = err
			if attempt == attempts {
				break
			}
			time.Sleep(delay)
			continue
		}
		return nil
	}
	return lastErr
}
