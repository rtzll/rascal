package main

import (
	"log"
	"os"
	"time"

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
	log.Printf("[%s] starting rascal-runner %s", time.Now().UTC().Format(time.RFC3339), worker.BuildInfoSummary())
	if err := worker.Run(); err != nil {
		log.Printf("[%s] run failed: %v", time.Now().UTC().Format(time.RFC3339), err)
		os.Exit(1)
	}
}
