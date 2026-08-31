package promote

// Directory client calls (v1 phase 1 — edge naming, CONTRACTS-CP):
//
//   - GetDirectory: the periodic FULL-TABLE pull (GET /api/v1/directory,
//     Bearer collector key) with a conditional fetch — If-None-Match with the
//     stored ETag, 304 → nothing to do. Never called per cache miss.
//   - SubmitDirectoryName: one OPT-IN rename suggestion (POST
//     /api/v1/directory/submissions, Bearer collector key). Only ever sent when
//     the user ticked the per-mapping checkbox; a failure never fails the
//     local save.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/flanj-io/collector/internal/model"
)

// GetDirectory fetches the full directory table. etag, when non-empty, is sent
// as If-None-Match; a 304 returns (nil, etag, 304, nil). A 200 returns the raw
// body and the response's ETag. Any other status is a *CPError.
func (c *Client) GetDirectory(ctx context.Context, etag string) (body []byte, newETag string, status int, err error) {
	const path = "/api/v1/directory"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.bearer())
	req.Header.Set("X-Flanj-Collector-Version", c.CollectorVersion)
	req.Header.Set("X-Flanj-Schema-Version", fmt.Sprintf("%d", model.SchemaVersion))
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, etag, resp.StatusCode, nil
	case http.StatusOK:
		return raw, resp.Header.Get("ETag"), resp.StatusCode, nil
	}
	ce := &CPError{Status: resp.StatusCode, Path: "GET " + path}
	var eb struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(bytes.TrimSpace(raw), &eb) == nil {
		ce.Code, ce.Message = strings.TrimSpace(eb.Error), strings.TrimSpace(eb.Message)
	}
	return nil, "", resp.StatusCode, ce
}

// DirectorySubmissionRequest is POST /api/v1/directory/submissions — one
// opt-in mapping suggestion: the registrable domain and the proposed name,
// nothing else.
type DirectorySubmissionRequest struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
}

// SubmitDirectoryName sends one opt-in suggestion with the collector key.
// 201/202/200 all count as accepted (the CP stores it as a pending row).
func (c *Client) SubmitDirectoryName(ctx context.Context, req DirectorySubmissionRequest) (int, error) {
	return c.do(ctx, http.MethodPost, "/api/v1/directory/submissions", c.bearer(), req, nil,
		http.StatusCreated, http.StatusAccepted, http.StatusOK)
}
