package logs

import (
	"bufio"
	"fmt"
	"os"
)

func Tail(path string, maxLines int) ([]string, error) {
	if maxLines <= 0 {
		maxLines = 100
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lines := make([]string, 0, maxLines)
	for scanner.Scan() {
		if len(lines) < maxLines {
			lines = append(lines, scanner.Text())
			continue
		}
		copy(lines, lines[1:])
		lines[len(lines)-1] = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read log file: %w", err)
	}

	return lines, nil
}
