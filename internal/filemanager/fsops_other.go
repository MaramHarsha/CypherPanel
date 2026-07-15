//go:build !linux

package filemanager

// Handler is a non-Linux stub: the file manager only operates on managed Linux
// servers (as the account uid/gid), which dev machines can't emulate safely.
type Handler struct {
	HomeRoot func(username string) string
}

func (h *Handler) Handle(req Request) Response {
	return Response{Error: "file manager is only available on Linux servers"}
}
