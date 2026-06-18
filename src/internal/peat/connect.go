// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package peat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// peat serves Connect (HTTP+JSON), gRPC, and gRPC-Web on a single port. We use
// the Connect/JSON unary wire — the exact shape validated in M2 (uint64 fields
// as JSON strings, the `bytes` sha256 field as base64, enums by name).

const (
	svcPath             = "/peat.sidecar.v1.PeatSidecar/"
	defaultPollInterval = 2 * time.Second
)

// New dials the peat sidecar's Connect endpoint at address (host:port, e.g.
// "localhost:50051").
func New(address string) (Client, error) {
	base := address
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	return &connectClient{
		baseURL:      strings.TrimRight(base, "/"),
		http:         &http.Client{},
		pollInterval: defaultPollInterval,
		bundles:      map[string][]string{},
	}, nil
}

type connectClient struct {
	baseURL      string
	http         *http.Client
	pollInterval time.Duration

	mu      sync.Mutex
	bundles map[string][]string // bundleID -> distributionIDs (from SendAttachments)
}

// call issues a Connect unary RPC (POST + JSON). On a non-2xx response it
// surfaces the Connect error envelope ({"code","message"}).
func (c *connectClient) call(ctx context.Context, method string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("peat: marshal %s: %w", method, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+svcPath+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("peat: %s: %w", method, err)
	}
	defer httpResp.Body.Close()

	rb, _ := io.ReadAll(httpResp.Body)
	if httpResp.StatusCode/100 != 2 {
		var cerr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(rb, &cerr)
		if cerr.Code != "" || cerr.Message != "" {
			return fmt.Errorf("peat: %s: %s: %s", method, cerr.Code, cerr.Message)
		}
		return fmt.Errorf("peat: %s: http %d: %s", method, httpResp.StatusCode, string(rb))
	}
	if resp != nil {
		if err := json.Unmarshal(rb, resp); err != nil {
			return fmt.Errorf("peat: unmarshal %s: %w", method, err)
		}
	}
	return nil
}

func (c *connectClient) Status(ctx context.Context) (NodeStatus, error) {
	var r struct {
		NodeID         string `json:"nodeId"`
		EndpointAddr   string `json:"endpointAddr"`
		SyncActive     bool   `json:"syncActive"`
		ConnectedPeers uint32 `json:"connectedPeers"`
		Phase          string `json:"phase"`
	}
	if err := c.call(ctx, "GetStatus", struct{}{}, &r); err != nil {
		return NodeStatus{}, err
	}
	return NodeStatus(r), nil
}

func (c *connectClient) EnsurePeer(ctx context.Context, endpointID string, addresses []string, relayURL string) error {
	req := map[string]any{"endpointId": endpointID}
	if len(addresses) > 0 {
		req["addresses"] = addresses
	}
	if relayURL != "" {
		req["relayUrl"] = relayURL
	}
	// ConnectPeer is idempotent on the peat side; re-issuing a known peer is a no-op.
	return c.call(ctx, "ConnectPeer", req, nil)
}

func (c *connectClient) SendAttachments(ctx context.Context, bundleID string, files []FileSpec, scope Scope, priority Priority) (SendResult, error) {
	var scopeJSON map[string]any
	switch {
	case scope.AllNodes:
		scopeJSON = map[string]any{"allNodes": map[string]any{}}
	case len(scope.NodeIDs) > 0:
		scopeJSON = map[string]any{"nodeList": map[string]any{"nodeIds": scope.NodeIDs}}
	default:
		return SendResult{}, fmt.Errorf("peat: SendAttachments requires a scope (AllNodes or NodeIDs)")
	}

	jf := make([]map[string]any, 0, len(files))
	for _, f := range files {
		m := map[string]any{
			"rootName":     f.RootName,
			"relativePath": f.RelativePath,
			"sizeBytes":    strconv.FormatUint(f.Size, 10), // uint64 -> JSON string
			"sha256":       base64.StdEncoding.EncodeToString(f.SHA256[:]),
		}
		if f.ContentType != "" {
			m["contentType"] = f.ContentType
		}
		if f.DisplayName != "" {
			m["displayName"] = f.DisplayName
		}
		jf = append(jf, m)
	}
	req := map[string]any{
		"files":    jf,
		"scope":    scopeJSON,
		"priority": protoPriority(priority),
	}
	if bundleID != "" {
		req["bundleId"] = bundleID
	}

	var r struct {
		BundleID string `json:"bundleId"`
		Handles  []struct {
			FileIndex      uint32 `json:"fileIndex"`
			BlobToken      string `json:"blobToken"`
			DistributionID string `json:"distributionId"`
		} `json:"handles"`
	}
	if err := c.call(ctx, "SendAttachments", req, &r); err != nil {
		return SendResult{}, err
	}

	res := SendResult{BundleID: r.BundleID}
	dists := make([]string, 0, len(r.Handles))
	for _, h := range r.Handles {
		res.Handles = append(res.Handles, AttachmentHandle{
			FileIndex: h.FileIndex, BlobToken: h.BlobToken, DistributionID: h.DistributionID,
		})
		dists = append(dists, h.DistributionID)
	}
	c.mu.Lock()
	c.bundles[r.BundleID] = dists
	c.mu.Unlock()
	return res, nil
}

func (c *connectClient) GetDistribution(ctx context.Context, distributionID string) (Progress, error) {
	var r struct {
		Status           string `json:"status"`
		BytesTransferred string `json:"bytesTransferred"`
		BytesTotal       string `json:"bytesTotal"`
		Error            string `json:"error"`
	}
	if err := c.call(ctx, "GetAttachmentDistribution", map[string]any{"distributionId": distributionID}, &r); err != nil {
		return Progress{}, err
	}
	return Progress{
		DistributionID:   distributionID,
		Status:           fromProtoStatus(r.Status),
		BytesTransferred: parseU64(r.BytesTransferred),
		BytesTotal:       parseU64(r.BytesTotal),
		Err:              r.Error,
	}, nil
}

// WaitBundle polls every distribution in the bundle until all are terminal.
// It relies on the distribution IDs recorded by SendAttachments in this client;
// after a process restart, the reconciler re-sends (idempotent) to repopulate.
// TODO(M3+): switch to the SubscribeAttachmentBundle server stream and make the
// reconciler time-box this with RequeueAfter for long DDIL waits.
func (c *connectClient) WaitBundle(ctx context.Context, bundleID string) ([]Progress, error) {
	c.mu.Lock()
	dists := append([]string(nil), c.bundles[bundleID]...)
	c.mu.Unlock()
	if len(dists) == 0 {
		return nil, fmt.Errorf("peat: no known distributions for bundle %q (re-send to repopulate)", bundleID)
	}

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		progs := make([]Progress, 0, len(dists))
		allTerminal := true
		for _, d := range dists {
			p, err := c.GetDistribution(ctx, d)
			if err != nil {
				return nil, err
			}
			progs = append(progs, p)
			if !p.Status.Terminal() {
				allTerminal = false
			}
		}
		if allTerminal {
			return progs, nil
		}
		select {
		case <-ctx.Done():
			return progs, ctx.Err()
		case <-ticker.C:
		}
	}
}

// --- Document plane (CRDT) ---
//
// Documents are JSON blobs in named collections, synced via CRDT over the mesh.
// json_data is itself a JSON-encoded string on the wire; we pass the manifest
// bytes through verbatim.

func (c *connectClient) PutDocument(ctx context.Context, collection, docID string, jsonData []byte) error {
	req := map[string]any{
		"collection": collection,
		"docId":      docID,
		"jsonData":   string(jsonData),
	}
	return c.call(ctx, "PutDocument", req, nil)
}

func (c *connectClient) GetDocument(ctx context.Context, collection, docID string) ([]byte, bool, error) {
	// jsonData is `optional string`; proto3-JSON omits it entirely when the
	// document is absent, so a nil pointer means not-found.
	var r struct {
		JSONData *string `json:"jsonData"`
	}
	if err := c.call(ctx, "GetDocument", map[string]any{"collection": collection, "docId": docID}, &r); err != nil {
		return nil, false, err
	}
	if r.JSONData == nil {
		return nil, false, nil
	}
	return []byte(*r.JSONData), true, nil
}

func (c *connectClient) ListDocuments(ctx context.Context, collection string) ([]string, error) {
	var r struct {
		DocIDs []string `json:"docIds"`
	}
	if err := c.call(ctx, "ListDocuments", map[string]any{"collection": collection}, &r); err != nil {
		return nil, err
	}
	return r.DocIDs, nil
}

func (c *connectClient) Close() error { return nil }

func protoPriority(p Priority) string {
	switch p {
	case PriorityLow:
		return "ATTACHMENT_PRIORITY_LOW"
	case PriorityRoutine:
		return "ATTACHMENT_PRIORITY_ROUTINE"
	case PriorityPriority:
		return "ATTACHMENT_PRIORITY_PRIORITY"
	case PriorityCritical:
		return "ATTACHMENT_PRIORITY_CRITICAL"
	default:
		return "ATTACHMENT_PRIORITY_BULK"
	}
}

func fromProtoStatus(s string) DistributionStatus {
	switch s {
	case "DISTRIBUTION_STATUS_IN_PROGRESS":
		return StatusInProgress
	case "DISTRIBUTION_STATUS_COMPLETED":
		return StatusCompleted
	case "DISTRIBUTION_STATUS_FAILED":
		return StatusFailed
	case "DISTRIBUTION_STATUS_CANCELLED":
		return StatusCancelled
	default:
		return StatusPending
	}
}

func parseU64(s string) uint64 {
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}
