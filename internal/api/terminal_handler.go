package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"

	"github.com/MaramHarsha/CypherPanel/internal/auth"
	"github.com/MaramHarsha/CypherPanel/internal/store"
	"github.com/MaramHarsha/CypherPanel/internal/terminal"
)

// TerminalHandler bridges a browser WebSocket to a PTY the account's agent
// spawns as the account user. It authenticates from a query token (a browser
// can't set Authorization on a WebSocket) and scopes to the caller's account.
type TerminalHandler struct {
	Accounts *store.Accounts
	Tokens   *auth.TokenService
	NC       *nats.Conn
}

// Serve upgrades to a WebSocket and streams a terminal session.
//
//	@Summary  Open a web terminal for an account (WebSocket)
//	@Tags     admin
//	@Router   /admin/accounts/{id}/terminal [get]
func (h *TerminalHandler) Serve(c *gin.Context) {
	// WebSocket auth: token in the query string, validated like a Bearer token.
	claims, err := h.Tokens.ParseAccess(c.Query("token"))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or missing token"})
		return
	}
	account, err := h.Accounts.GetByID(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !auth.CanAct(claims, account.ResellerID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// InsecureSkipVerify: dev accepts cross-origin (localhost:3000 → :8080). In
	// production the UI is same-origin behind the proxy; restrict origins there.
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "closing")

	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()

	sessionID := randHex(16)

	// Agent → browser: forward every out message to the WebSocket.
	sub, err := h.NC.Subscribe(terminal.OutSubject(sessionID), func(m *nats.Msg) {
		if werr := conn.Write(ctx, websocket.MessageText, m.Data); werr != nil {
			cancel()
		}
	})
	if err != nil {
		return
	}
	defer sub.Unsubscribe()

	// Ask the agent to open the PTY as the account's system user.
	start, _ := json.Marshal(terminal.StartRequest{
		SessionID: sessionID, Username: account.SystemUsername, Cols: 80, Rows: 24,
	})
	if perr := h.NC.Publish(terminal.CtrlSubject(account.ServerID), start); perr != nil {
		return
	}

	// Browser → agent: forward every WebSocket message (already framed terminal
	// messages) to the session's input subject.
	for {
		_, data, rerr := conn.Read(ctx)
		if rerr != nil {
			break
		}
		_ = h.NC.Publish(terminal.InSubject(sessionID), data)
	}

	// Tell the agent to end the session, then close cleanly.
	closeMsg, _ := json.Marshal(terminal.Msg{Type: "close"})
	_ = h.NC.Publish(terminal.InSubject(sessionID), closeMsg)
	conn.Close(websocket.StatusNormalClosure, "")
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
