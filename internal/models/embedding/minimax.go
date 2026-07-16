package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

const minimaxEmbedPath = "/v1/embeddings"

// MiniMaxIntlBaseURL is MiniMax's international embedding service
// (https://api.minimax.chat/v1). The endpoint accepts POST /v1/embeddings
// with a non-OpenAI request body that uses `texts` + a mandatory `type`
// field, and returns the vectors array.
const MiniMaxIntlBaseURL = "https://api.minimax.chat/v1"

// MiniMaxEmbedder implements text vectorization against MiniMax's native
// embedding API. MiniMax does NOT use the OpenAI-compatible `input` field;
// its request body has `texts` (array) and `type` (string). The official
// sample from MiniMax uses `type: "db"` for offline index ingestion.
//
// Reference body:
//
//	POST /v1/embeddings?GroupId=<optional>
//	{
//	    "model": "embo-01",
//	    "texts": ["..."],
//	    "type": "db"
//	}
//
// Reference response:
//
//	{
//	    "vectors": [[...]],          // list of float vectors
//	    "total_tokens": 8,
//	    "base_resp": {"status_code": 0, "status_msg": "success"}
//	}
//
// Like other providers in this package, the `baseURL` configured in the UI
// must be just the API prefix (e.g. https://api.minimax.chat/v1); this
// embedder appends "/embeddings" itself. If the user accidentally pastes
// the full endpoint (…/v1/embeddings), we strip the suffix defensively.
type MiniMaxEmbedder struct {
	apiKey        string
	baseURL       string
	modelName     string
	modelID       string
	dimensions    int
	embedType     string
	groupID       string
	httpClient    *http.Client
	maxRetries    int
	customHeaders map[string]string
	EmbedderPooler
}

// MiniMaxEmbedRequest is the request body posted to /v1/embeddings.
type MiniMaxEmbedRequest struct {
	Model string   `json:"model"`
	Texts []string `json:"texts"`
	Type  string   `json:"type"`
}

// MiniMaxEmbedResponse is the parsed response from /v1/embeddings.
type MiniMaxEmbedResponse struct {
	Vectors     [][]float32 `json:"vectors"`
	TotalTokens int         `json:"total_tokens"`
	BaseResp    struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

// NewMiniMaxEmbedder constructs a MiniMaxEmbedder from a config block.
// ExtraConfig (optional) recognises two keys:
//
//	"type"     — overrides the default `type` value sent in the body
//	             (default "db" per MiniMax official sample).
//	"group_id" — when set, appended to the URL as ?GroupId=...
//	             (GroupId is the multi-tenant grouping from MiniMax console).
func NewMiniMaxEmbedder(config Config, pooler EmbedderPooler) (*MiniMaxEmbedder, error) {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = MiniMaxIntlBaseURL
	}
	// Defence in depth: if the user pasted the full endpoint into baseURL,
	// strip "/embeddings" so we don't end up with /v1/embeddings/embeddings.
	baseURL = strings.TrimSuffix(baseURL, "/embeddings")

	if config.APIKey == "" {
		return nil, fmt.Errorf("MiniMax embedder: API key is required")
	}
	if config.ModelName == "" {
		return nil, fmt.Errorf("MiniMax embedder: model name is required")
	}

	embedType := "db"
	groupID := ""
	if config.ExtraConfig != nil {
		if v := strings.TrimSpace(config.ExtraConfig["type"]); v != "" {
			embedType = v
		}
		if v := strings.TrimSpace(config.ExtraConfig["group_id"]); v != "" {
			groupID = v
		}
	}

	if err := validateEmbeddingBaseURL(baseURL); err != nil {
		return nil, err
	}

	return &MiniMaxEmbedder{
		apiKey:         config.APIKey,
		baseURL:        baseURL,
		modelName:      config.ModelName,
		modelID:        config.ModelID,
		dimensions:     config.Dimensions,
		embedType:      embedType,
		groupID:        groupID,
		httpClient:     newEmbeddingHTTPClient(60 * time.Second),
		maxRetries:     3,
		customHeaders:  nil,
		EmbedderPooler: pooler,
	}, nil
}

// SetCustomHeaders mirrors the setter on other embedders so callers can
// inject gateway/tracing headers without touching the SDK.
func (e *MiniMaxEmbedder) SetCustomHeaders(headers map[string]string) {
	e.customHeaders = headers
}

// SetEmbedType overrides the `type` value MiniMax expects in the body.
// This is useful because MiniMax distinguishes query / document / db
// embedding spaces; the right value depends on the call site (query-time
// retrieval vs offline indexing).
func (e *MiniMaxEmbedder) SetEmbedType(t string) {
	e.embedType = t
}

// Embed is typically the query-time path. We force `type=query` for the
// duration of this call so the resulting vector lives in the query
// embedding space. The previous type is restored on return.
func (e *MiniMaxEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	prev := e.embedType
	e.embedType = "query"
	defer func() { e.embedType = prev }()

	results, err := e.BatchEmbed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("MiniMax embedder: empty response")
	}
	return results[0], nil
}

// BatchEmbed is the ingestion / index path. Force `type=db` (per the
// MiniMax official sample) for the duration of this call, then restore.
func (e *MiniMaxEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	prev := e.embedType
	e.embedType = "db"
	defer func() { e.embedType = prev }()
	return e.embedWithType(ctx, texts)
}

func (e *MiniMaxEmbedder) embedWithType(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := MiniMaxEmbedRequest{
		Model: e.modelName,
		Texts: texts,
		Type:  e.embedType,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := e.baseURL + minimaxEmbedPath
	if e.groupID != "" {
		url = url + "?GroupId=" + e.groupID
	}

	resp, err := e.doRequestWithRetry(ctx, url, jsonData)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyStr := string(body)
		if len(bodyStr) > 1000 {
			bodyStr = bodyStr[:1000] + "... (truncated)"
		}
		return nil, fmt.Errorf("MiniMax EmbedBatch API error: Http Status %s, Response: %s",
			resp.Status, bodyStr)
	}

	var response MiniMaxEmbedResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if response.BaseResp.StatusCode != 0 {
		return nil, fmt.Errorf("MiniMax API error: status_code=%d, status_msg=%s",
			response.BaseResp.StatusCode, response.BaseResp.StatusMsg)
	}

	if len(response.Vectors) != len(texts) {
		return nil, fmt.Errorf("MiniMax embedder: expected %d vectors, got %d",
			len(texts), len(response.Vectors))
	}

	return response.Vectors, nil
}

func (e *MiniMaxEmbedder) doRequestWithRetry(ctx context.Context, url string, jsonData []byte) (*http.Response, error) {
	var resp *http.Response
	var err error

	for i := 0; i <= e.maxRetries; i++ {
		if i > 0 {
			backoffTime := time.Duration(1<<uint(i-1)) * time.Second
			if backoffTime > 10*time.Second {
				backoffTime = 10 * time.Second
			}
			logger.GetLogger(ctx).Infof("MiniMaxEmbedder retrying request (%d/%d), waiting %v",
				i, e.maxRetries, backoffTime)
			select {
			case <-time.After(backoffTime):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
		if err != nil {
			logger.GetLogger(ctx).Errorf("MiniMaxEmbedder failed to create request: %v", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
		secutils.ApplyCustomHeaders(req, e.customHeaders)

		resp, err = e.httpClient.Do(req)
		if err == nil {
			return resp, nil
		}
		logger.GetLogger(ctx).Errorf("MiniMaxEmbedder request failed (attempt %d/%d): %v",
			i+1, e.maxRetries+1, err)
	}

	return nil, err
}

// GetModelName returns the model name.
func (e *MiniMaxEmbedder) GetModelName() string { return e.modelName }

// GetModelID returns the model ID.
func (e *MiniMaxEmbedder) GetModelID() string { return e.modelID }

// GetDimensions returns the configured dimensions, falling back to 0
// when the caller did not specify one.
func (e *MiniMaxEmbedder) GetDimensions() int { return e.dimensions }
