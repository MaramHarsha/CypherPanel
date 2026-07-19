package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// SaveImage exports an image as a tar stream.
func (c *Client) SaveImage(ctx context.Context, tag string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/images/"+tag+"/get", nil, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// LoadImage loads an image from a tar stream.
func (c *Client) LoadImage(ctx context.Context, stream io.Reader, quiet bool) error {
	q := url.Values{}
	if quiet {
		q.Set("quiet", "true")
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/load", q, stream, "application/x-tar")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// We consume the body but don't strictly parse JSON unless we need to check errors deeply.
	// The response is a stream of JSON messages.
	dec := json.NewDecoder(resp.Body)
	for {
		var line struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&line); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("engine: reading load stream: %w", err)
		}
		if line.Error != "" {
			return fmt.Errorf("engine: load failed: %s", strings.TrimSpace(line.Error))
		}
	}
}
