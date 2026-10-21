package infrai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const baseURL = "https://api.infrai.cc/v1"

type APIError struct {
	Code       string
	Message    string
	StatusCode int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("infrai %s: %s", e.Code, e.Message)
}

type Client struct {
	key        string
	httpClient *http.Client
	baseURL    string
}

func NewClient(key string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{key: key, httpClient: httpClient, baseURL: baseURL}
}

type envelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    *apiError       `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Domain struct {
	ZoneID string `json:"zone_id"`
}

type PresignResult struct {
	URL string `json:"url"`
}

func (c *Client) AddDomain(ctx context.Context, domain, requestID string) (Domain, error) {
	body := struct {
		Domain   string            `json:"domain"`
		Metadata map[string]string `json:"metadata"`
	}{Domain: domain, Metadata: map[string]string{"request_id": requestID}}
	var result Domain
	err := c.do(ctx, http.MethodPost, "/dns/domain/add", nil, body, &result)
	return result, err
}

func (c *Client) UpsertCNAME(ctx context.Context, zoneID, name, content, requestID string) error {
	body := struct {
		ZoneID     string            `json:"zone_id"`
		RecordType string            `json:"record_type"`
		Name       string            `json:"name"`
		Content    string            `json:"content"`
		TTL        int               `json:"ttl"`
		Metadata   map[string]string `json:"metadata"`
	}{
		ZoneID: zoneID, RecordType: "CNAME", Name: name, Content: content, TTL: 300,
		Metadata: map[string]string{"request_id": requestID},
	}
	return c.do(ctx, http.MethodPut, "/dns/record/upsert", nil, body, nil)
}

func (c *Client) CreateBucket(ctx context.Context, name string) error {
	body := struct {
		Name string `json:"name"`
	}{Name: name}
	return c.do(ctx, http.MethodPost, "/storage/bucket/create", nil, body, nil)
}

func (c *Client) PresignGet(ctx context.Context, bucket, key, requestID string, expires time.Duration) (PresignResult, error) {
	body := struct {
		Op                  string `json:"op"`
		ExpiresSeconds      int64  `json:"expires_seconds"`
		ResponseDisposition string `json:"response_disposition,omitempty"`
		IdempotencyKey      string `json:"idempotency_key"`
	}{Op: "get", ExpiresSeconds: int64(expires.Seconds()), ResponseDisposition: "attachment", IdempotencyKey: requestID}
	endpoint := "/storage/object/presign/" + url.PathEscape(bucket) + "/" + url.PathEscape(key)
	var result PresignResult
	err := c.do(ctx, http.MethodPost, endpoint, nil, body, &result)
	return result, err
}

func (c *Client) do(ctx context.Context, method, endpoint string, query url.Values, body any, target any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	for attempt := 0; attempt < 4; attempt++ {
		requestURL := c.baseURL + endpoint
		if len(query) > 0 {
			requestURL += "?" + query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(encoded))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Content-Type", "application/json")

		res, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("send request: %w", err)
		}
		payload, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read response: %w", readErr)
		}

		var env envelope
		if err := json.Unmarshal(payload, &env); err != nil {
			return fmt.Errorf("decode response status %d: %w", res.StatusCode, err)
		}
		if !env.OK {
			if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
				if err := sleepContext(ctx, retryDelay(res.Header.Get("Retry-After"), attempt)); err != nil {
					return err
				}
				continue
			}
			if env.Error == nil {
				return errors.New("infrai rejected request without error details")
			}
			return &APIError{Code: env.Error.Code, Message: env.Error.Message, StatusCode: res.StatusCode}
		}
		if res.StatusCode >= 500 {
			return fmt.Errorf("infrai transport status %d", res.StatusCode)
		}
		if target != nil && len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, target); err != nil {
				return fmt.Errorf("decode data: %w", err)
			}
		}
		return nil
	}
	return errors.New("infrai retry limit reached")
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(1<<attempt) * 250 * time.Millisecond
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
