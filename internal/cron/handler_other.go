//go:build !linux

package cron

// Handle is a non-Linux stub: cron management targets managed Linux servers.
func Handle(req Request) Response {
	return Response{Error: "cron is only available on Linux servers"}
}
