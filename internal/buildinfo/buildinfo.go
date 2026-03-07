package buildinfo

import (
	"fmt"
	"io"
	"strings"
)

const packagePath = "github.com/rtzll/rascal/internal/buildinfo"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func Summary() string {
	return fmt.Sprintf("version=%s commit=%s built=%s", Version, Commit, Date)
}

func Detailed() string {
	return fmt.Sprintf("%s (commit: %s, built: %s)", Version, Commit, Date)
}

func BinaryVersion(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return Detailed()
	}
	return fmt.Sprintf("%s %s", name, Detailed())
}

func IsVersionRequest(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch strings.TrimSpace(args[0]) {
	case "--version", "-version", "version":
		return true
	default:
		return false
	}
}

func PrintVersion(w io.Writer, name string) error {
	_, err := fmt.Fprintln(w, BinaryVersion(name))
	return err
}

func LinkerFlags(version, commit, date string, strip bool) string {
	flags := make([]string, 0, 5)
	if strip {
		flags = append(flags, "-s", "-w")
	}
	flags = append(flags,
		fmt.Sprintf("-X %s.Version=%s", packagePath, normalize(version, "dev")),
		fmt.Sprintf("-X %s.Commit=%s", packagePath, normalize(commit, "unknown")),
		fmt.Sprintf("-X %s.Date=%s", packagePath, normalize(date, "unknown")),
	)
	return strings.Join(flags, " ")
}

func normalize(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
