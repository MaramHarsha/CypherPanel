package rest

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/databases"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// --- DTOs ---

type databaseDTO struct {
	ID                 string     `json:"id"`
	EnvironmentID      string     `json:"environment_id"`
	Name               string     `json:"name"`
	Engine             string     `json:"engine"`
	Version            string     `json:"version"`
	ServerID           string     `json:"server_id"`
	CPULimit           *float64   `json:"cpu_limit,omitempty"`
	MemoryLimitMB      *int       `json:"memory_limit_mb,omitempty"`
	VolumeName         string     `json:"volume_name"`
	ExposePort         *int       `json:"expose_port,omitempty"`
	Network            string     `json:"network"`
	RootUser           string     `json:"root_user"`
	RootPassword       string     `json:"root_password,omitempty"` // only on create / reset
	RequirePassword    bool       `json:"require_password"`
	Status             string     `json:"status"`
	StatusDetail       string     `json:"status_detail,omitempty"`
	DesiredRevisionID  *string    `json:"desired_revision_id,omitempty"`
	ObservedRevisionID string     `json:"observed_revision_id,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func toDatabaseDTO(d domain.Database) databaseDTO {
	return databaseDTO{
		ID:                 d.ID,
		EnvironmentID:      d.EnvironmentID,
		Name:               d.Name,
		Engine:             string(d.Engine),
		Version:            d.Version,
		ServerID:           d.ServerID,
		CPULimit:           d.CPULimit,
		MemoryLimitMB:      d.MemoryLimitMB,
		VolumeName:         d.VolumeName,
		ExposePort:         d.ExposePort,
		Network:            d.Network,
		RootUser:           d.RootUser,
		RootPassword:       "[sealed]", // never expose — rule 20
		RequirePassword:    d.RequirePassword,
		Status:             d.Status,
		StatusDetail:       d.StatusDetail,
		DesiredRevisionID:  d.DesiredRevisionID,
		ObservedRevisionID: d.ObservedRevisionID,
		CreatedAt:          d.CreatedAt,
		UpdatedAt:          d.UpdatedAt,
	}
}

// --- Request/Response types ---

type createDatabaseRequest struct {
	Name            string   `json:"name"`
	Engine          string   `json:"engine"`
	Version         string   `json:"version"`
	ServerID        string   `json:"server_id"`
	CPULimit        *float64 `json:"cpu_limit,omitempty"`
	MemoryLimitMB   *int     `json:"memory_limit_mb,omitempty"`
	ExposePort      *int     `json:"expose_port,omitempty"`
	RequirePassword bool     `json:"require_password"`
}

type createDatabaseResponse struct {
	Database     databaseDTO `json:"database"`
	RootPassword string      `json:"root_password"` // shown once
}

type patchDatabaseRequest struct {
	Name          *string  `json:"name,omitempty"`
	Version       *string  `json:"version,omitempty"`
	CPULimit      *float64 `json:"cpu_limit,omitempty"`
	MemoryLimitMB *int     `json:"memory_limit_mb,omitempty"`
	ExposePort    *int     `json:"expose_port,omitempty"`
}

type resetPasswordResponse struct {
	RootPassword string `json:"root_password"`
}

type connectionInfoResponse struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user,omitempty"`
	PasswordHint string `json:"password_hint"`
	InternalHost string `json:"internal_host"`
}

// --- Handlers ---

func (a *API) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	envID := r.PathValue("id")
	var req createDatabaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	db, rootPwd, err := a.deps.Databases.Create(r.Context(), envID, databases.CreateInput{
		Name:            req.Name,
		Engine:          req.Engine,
		Version:         req.Version,
		ServerID:        req.ServerID,
		CPULimit:        req.CPULimit,
		MemoryLimitMB:   req.MemoryLimitMB,
		ExposePort:      req.ExposePort,
		RequirePassword: req.RequirePassword,
	})
	if err != nil {
		handleDatabaseError(a, w, err, "creating database")
		return
	}

	dto := toDatabaseDTO(db)
	dto.RootPassword = rootPwd // show once
	writeJSON(w, http.StatusCreated, createDatabaseResponse{
		Database:     dto,
		RootPassword: rootPwd,
	})
}

func (a *API) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	envID := r.PathValue("id")
	dbs, err := a.deps.Databases.List(r.Context(), envID)
	if err != nil {
		a.deps.Log.Error("listing databases", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list databases")
		return
	}
	out := make([]databaseDTO, 0, len(dbs))
	for _, d := range dbs {
		out = append(out, toDatabaseDTO(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetDatabase(w http.ResponseWriter, r *http.Request) {
	db, err := a.deps.Databases.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "database not found")
			return
		}
		a.deps.Log.Error("getting database", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get database")
		return
	}
	writeJSON(w, http.StatusOK, toDatabaseDTO(db))
}

func (a *API) handlePatchDatabase(w http.ResponseWriter, r *http.Request) {
	var req patchDatabaseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	db, err := a.deps.Databases.Update(r.Context(), r.PathValue("id"), databases.UpdateInput{
		Name:          req.Name,
		Version:       req.Version,
		CPULimit:      req.CPULimit,
		MemoryLimitMB: req.MemoryLimitMB,
		ExposePort:    req.ExposePort,
	})
	if err != nil {
		handleDatabaseError(a, w, err, "updating database")
		return
	}
	writeJSON(w, http.StatusOK, toDatabaseDTO(db))
}

func (a *API) handleDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	deleteVolume := r.URL.Query().Get("delete_volume") == "true"
	if err := a.deps.Databases.Delete(r.Context(), r.PathValue("id"), deleteVolume); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "database not found")
			return
		}
		a.deps.Log.Error("deleting database", "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete database")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleStopDatabase(w http.ResponseWriter, r *http.Request) {
	if err := a.deps.Databases.Stop(r.Context(), r.PathValue("id")); err != nil {
		handleDatabaseError(a, w, err, "stopping database")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) handleStartDatabase(w http.ResponseWriter, r *http.Request) {
	if err := a.deps.Databases.Start(r.Context(), r.PathValue("id")); err != nil {
		handleDatabaseError(a, w, err, "starting database")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) handleResetDatabasePassword(w http.ResponseWriter, r *http.Request) {
	pwd, err := a.deps.Databases.ResetPassword(r.Context(), r.PathValue("id"))
	if err != nil {
		handleDatabaseError(a, w, err, "resetting password")
		return
	}
	writeJSON(w, http.StatusOK, resetPasswordResponse{RootPassword: pwd})
}

func (a *API) handleDatabaseConnectionInfo(w http.ResponseWriter, r *http.Request) {
	db, err := a.deps.Databases.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "database not found")
			return
		}
		a.deps.Log.Error("getting database for connection info", "error", err)
		writeError(w, http.StatusInternalServerError, "could not get database")
		return
	}

	containerName := "cypher-db-" + db.ID
	defaults := domain.EngineDefaults(db.Engine, db.Version)
	port := defaultPort(db.Engine)

	resp := connectionInfoResponse{
		InternalHost: containerName + "." + db.Network,
		Port:         port,
		User:         defaults.RootUser,
		PasswordHint: "[use the password from create or reset-password]",
	}
	if db.ExposePort != nil {
		resp.Host = db.ServerID // The operator needs the server's address
		resp.Port = *db.ExposePort
	}
	writeJSON(w, http.StatusOK, resp)
}

// defaultPort returns the well-known port for a database engine.
func defaultPort(engine domain.DbEngine) int {
	switch engine {
	case domain.EnginePostgreSQL:
		return 5432
	case domain.EngineMySQL, domain.EngineMariaDB:
		return 3306
	case domain.EngineMongoDB:
		return 27017
	case domain.EngineRedis, domain.EngineValkey:
		return 6379
	default:
		return 0
	}
}

// handleDatabaseError maps service errors to HTTP responses.
func handleDatabaseError(a *API, w http.ResponseWriter, err error, action string) {
	var ve *databases.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, databases.ErrServerNotFound):
		writeError(w, http.StatusBadRequest, "server not found")
	case errors.Is(err, databases.ErrEnvironmentNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "database not found")
	default:
		a.deps.Log.Error(action, "error", err)
		writeError(w, http.StatusInternalServerError, "could not complete "+action)
	}
}

// strconv import needed by other handlers in this package.
var _ = strconv.Itoa
