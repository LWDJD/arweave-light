package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/arweave-light/types"
)

// HTTPClient wraps an Arweave node's HTTP API.
type HTTPClient struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
}

// NewHTTPClient creates a new Arweave HTTP API client.
func NewHTTPClient(baseURL string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		baseURL: baseURL,
		timeout: timeout,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// SetTimeout updates the request timeout.
func (c *HTTPClient) SetTimeout(d time.Duration) {
	c.timeout = d
	c.httpClient.Timeout = d
}

// doGET performs a GET request and returns the response body.
func (c *HTTPClient) doGET(ctx context.Context, path string) ([]byte, error) {
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// GetInfo retrieves the current network info.
func (c *HTTPClient) GetInfo(ctx context.Context) (*types.ChainInfo, error) {
	body, err := c.doGET(ctx, "/info")
	if err != nil {
		return nil, err
	}

	var raw struct {
		Height  uint64 `json:"height"`
		Current string `json:"current"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse info: %w", err)
	}

	var h types.Hash
	if err := h.UnmarshalJSON([]byte(`"` + raw.Current + `"`)); err != nil {
		return nil, fmt.Errorf("parse current hash: %w", err)
	}

	return &types.ChainInfo{
		Height:      raw.Height,
		CurrentHash: h,
	}, nil
}

// GetBlockByHeight fetches a full block by height.
func (c *HTTPClient) GetBlockByHeight(ctx context.Context, height uint64) (*types.Block, error) {
	body, err := c.doGET(ctx, fmt.Sprintf("/block/height/%d", height))
	if err != nil {
		return nil, err
	}

	var block types.Block
	if err := json.Unmarshal(body, &block); err != nil {
		return nil, fmt.Errorf("parse block: %w", err)
	}
	return &block, nil
}

// GetBlockByHash fetches a block by its hash.
func (c *HTTPClient) GetBlockByHash(ctx context.Context, hash types.Hash) (*types.Block, error) {
	body, err := c.doGET(ctx, fmt.Sprintf("/block/hash/%s", hash.Base64()))
	if err != nil {
		return nil, err
	}

	var block types.Block
	if err := json.Unmarshal(body, &block); err != nil {
		return nil, fmt.Errorf("parse block: %w", err)
	}
	return &block, nil
}

// GetTransaction fetches a transaction by ID.
func (c *HTTPClient) GetTransaction(ctx context.Context, txID types.Hash) (*types.Transaction, error) {
	body, err := c.doGET(ctx, fmt.Sprintf("/tx/%s", txID.Base64()))
	if err != nil {
		return nil, err
	}

	var tx types.Transaction
	if err := json.Unmarshal(body, &tx); err != nil {
		return nil, fmt.Errorf("parse transaction: %w", err)
	}
	return &tx, nil
}

// GetTransactionData fetches the raw data for a transaction.
func (c *HTTPClient) GetTransactionData(ctx context.Context, txID types.Hash) ([]byte, error) {
	return c.doGET(ctx, fmt.Sprintf("/tx/%s/data", txID.Base64()))
}

// GetTxAnchor fetches the anchor for new transactions.
func (c *HTTPClient) GetTxAnchor(ctx context.Context) (types.Hash, error) {
	body, err := c.doGET(ctx, "/tx_anchor")
	if err != nil {
		return types.EmptyHash(), err
	}
	var anchor string
	if err := json.Unmarshal(body, &anchor); err != nil {
		return types.EmptyHash(), fmt.Errorf("parse anchor: %w", err)
	}
	var h types.Hash
	if err := h.UnmarshalJSON([]byte(`"` + anchor + `"`)); err != nil {
		return types.EmptyHash(), fmt.Errorf("parse anchor hash: %w", err)
	}
	return h, nil
}

// GetPeers retrieves the peer list from the node.
func (c *HTTPClient) GetPeers(ctx context.Context) ([]string, error) {
	body, err := c.doGET(ctx, "/peers")
	if err != nil {
		return nil, err
	}
	var peers []string
	if err := json.Unmarshal(body, &peers); err != nil {
		return nil, fmt.Errorf("parse peers: %w", err)
	}
	return peers, nil
}

// GetBlockHeader fetches the block header by height.
func (c *HTTPClient) GetBlockHeader(ctx context.Context, height uint64) (*types.BlockHeader, error) {
	block, err := c.GetBlockByHeight(ctx, height)
	if err != nil {
		return nil, err
	}
	h := block.Header()
	return &h, nil
}

// Ping checks if the node is reachable.
func (c *HTTPClient) Ping(ctx context.Context) error {
	_, err := c.doGET(ctx, "/info")
	return err
}
