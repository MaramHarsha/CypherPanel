// Package terminal carries the Core↔Agent protocol for the web terminal
// (SSH-in-browser). A session streams over NATS: Core opens a WebSocket to the
// browser and bridges it to a PTY the agent spawns AS THE ACCOUNT USER, so the
// shell has exactly that account's privileges (no root shell handed out).
package terminal

// StartRequest asks an agent to open a PTY session for an account user.
type StartRequest struct {
	SessionID string `json:"session_id"`
	Username  string `json:"username"`
	Cols      uint16 `json:"cols"`
	Rows      uint16 `json:"rows"`
}

// Msg is a framed terminal message in either direction.
//   - "data":  Data is raw terminal bytes (stdin from browser / stdout to browser)
//   - "resize": Cols/Rows carry the new window size
//   - "close":  the session is ending (agent → core when the shell exits)
type Msg struct {
	Type string `json:"type"`
	Data []byte `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// CtrlSubject is where an agent listens for new-session requests.
func CtrlSubject(serverID string) string { return "term.ctrl.server." + serverID }

// InSubject carries browser→shell messages for a session (agent subscribes).
func InSubject(sessionID string) string { return "term.in." + sessionID }

// OutSubject carries shell→browser messages for a session (core subscribes).
func OutSubject(sessionID string) string { return "term.out." + sessionID }
