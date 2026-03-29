package main

import (
	"time"

	"github.com/rtzll/rascal/internal/api"
)

type runLogsFollowRenderer struct {
	last      string
	useSince  bool
	sinceTime time.Time
}

func newRunLogsFollowRenderer(since time.Duration, now func() time.Time) runLogsFollowRenderer {
	if since <= 0 {
		return runLogsFollowRenderer{}
	}
	return runLogsFollowRenderer{
		useSince:  true,
		sinceTime: now().Add(-since),
	}
}

func (r *runLogsFollowRenderer) Render(payload api.RunLogsResponse) string {
	body := payload.Logs
	if r.useSince {
		body = filterLogsSince(body, r.sinceTime)
	}
	defer func() {
		r.last = body
	}()
	if body == r.last {
		return ""
	}
	if len(r.last) > 0 && len(body) >= len(r.last) && body[:len(r.last)] == r.last {
		return body[len(r.last):]
	}
	return body
}
