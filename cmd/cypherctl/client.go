package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// client is a thin REST client for CypherCore. It deliberately contains no
// business logic: every command maps to an endpoint the panel and the UI use
// too, so the CLI can never drift from the product's actual behaviour.
type client struct {
	baseURL string
	http    *http.Client

	accessToken  string
	refreshToken string
}

// credentials is the on-disk session. Only the refresh token is persisted; the
// access token is short-lived and re-minted per invocation.
type credentials struct {
	BaseURL      string `json:"base_url"`
	RefreshToken string `json:"refresh_token"`
}

// configPath resolves the credentials file, honouring XDG on Unix and
// APPDATA on Windows rather than hardcoding a home-relative path.
func configPath() (string, error) {
	if v := os.Getenv("CYPHERCTL_CONFIG"); v != "" {
		return v, nil
	}
	var base string
	switch {
	case runtime.GOOS == "windows":
		base = os.Getenv("APPDATA")
	default:
		base = os.Getenv("XDG_CONFIG_HOME")
	}
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cypherpanel", "credentials.json"), nil
}

func loadCredentials() (*credentials, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("not logged in (run `cypherctl login`): %w", err)
	}
	var c credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("credentials file is corrupt; run `cypherctl login` again")
	}
	return &c, nil
}

func saveCredentials(c credentials) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600: a refresh token is a live credential.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	return nil
}

func clearCredentials() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// newClient builds a client from the saved session, redeeming the refresh
// token for a fresh access token.
func newClient(ctx context.Context) (*client, error) {
	creds, err := loadCredentials()
	if err != nil {
		return nil, err
	}
	c := &client{
		baseURL: strings.TrimRight(creds.BaseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	if err := c.refresh(ctx, creds.RefreshToken); err != nil {
		return nil, err
	}
	return c, nil
}

type tokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// refresh redeems a refresh token. Refresh tokens are single-use and rotated
// server-side, so the new one must be persisted immediately or the next
// invocation would present an already-spent token.
func (c *client) refresh(ctx context.Context, refreshToken string) error {
	var out tokenPair
	if err := c.post(ctx, "/auth/refresh", map[string]string{"refresh_token": refreshToken}, &out); err != nil {
		return fmt.Errorf("session expired (run `cypherctl login`): %w", err)
	}
	c.accessToken, c.refreshToken = out.AccessToken, out.RefreshToken
	return saveCredentials(credentials{BaseURL: c.baseURL, RefreshToken: out.RefreshToken})
}

// login exchanges username/password for a session and persists it.
func login(ctx context.Context, baseURL, username, password string) error {
	c := &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	var out tokenPair
	if err := c.post(ctx, "/auth/login", map[string]string{
		"username": username, "password": password,
	}, &out); err != nil {
		return err
	}
	return saveCredentials(credentials{BaseURL: c.baseURL, RefreshToken: out.RefreshToken})
}

// apiError carries the server's own error message so the CLI reports what the
// API actually said rather than a generic status code.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("request failed (%d)", e.Status)
	}
	return e.Message
}

func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api/v1"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("contacting %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return &apiError{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}

func (c *client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *client) delete(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// q builds a query string from non-empty pairs.
func q(pairs ...string) string {
	if len(pairs)%2 != 0 {
		panic("q: pairs must be key/value")
	}
	v := url.Values{}
	for i := 0; i < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			v.Set(pairs[i], pairs[i+1])
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}
