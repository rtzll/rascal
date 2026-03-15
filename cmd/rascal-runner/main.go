package main

import (
	"log"
	"os"

	"github.com/rtzll/rascal/internal/worker"
)

var (
	buildVersion = "dev"
	buildCommit  = "unknown"
	buildTime    = "unknown"
)

func syncBuildInfo() {
	worker.BuildVersion = buildVersion
	worker.BuildCommit = buildCommit
	worker.BuildTime = buildTime
}

func main() {
	log.SetFlags(0)
	syncBuildInfo()
	log.Printf("[%s] starting rascal-runner %s", nowUTC(), buildInfoSummary())
	if err := run(); err != nil {
		log.Printf("[%s] run failed: %v", nowUTC(), err)
		os.Exit(1)
	}
}
