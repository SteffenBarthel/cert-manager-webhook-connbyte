// Package connbyte is a minimal client for the ConnByte DNS API
// (https://api.connbyte.com/). It only covers the calls needed to solve
// ACME DNS-01 challenges.
package connbyte

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public ConnByte API endpoint.
const DefaultBaseURL = "https://api.connbyte.com"

const (
	// MinTTL is the smallest TTL accepted by the ConnByte API.
	MinTTL = 60
	// MaxTTL is the largest TTL accepted by the ConnByte API.
	MaxTTL = 86400
)

const maxResponseSize = 10 << 20

// Client talks to the ConnByte DNS API with a bearer token.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	UserAgent  string
}

// Zone is a DNS zone of the account.
type Zone struct {
	ID   string `json:"id"`
	Zone string `json:"zone"`
}

// Record is a DNS record as returned by the API.
type Record struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
}

// RecordInput is the payload to create a record.
type RecordInput struct {
	ZoneID   string `json:"zoneId"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
	Disabled bool   `json:"disabled"`
}

type status struct {
	Code    int    `json:"code"`
	Title   string `json:"title"`
	Message string `json:"message"`
	LogID   string `json:"logId"`
}

type envelope struct {
	Status status              `json:"status"`
	Data   json.RawMessage     `json:"data"`
	Field  []map[string]string `json:"field"`
}

// APIError is returned when the API answers with an error status.
type APIError struct {
	HTTPStatus int
	Code       int
	Title      string
	Message    string
	LogID      string
	Fields     []map[string]string
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "connbyte: API error %d", e.Code)
	if e.Title != "" {
		fmt.Fprintf(&b, " %s", e.Title)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	for _, f := range e.Fields {
		for k, v := range f {
			fmt.Fprintf(&b, " [%s: %s]", k, v)
		}
	}
	if e.LogID != "" {
		fmt.Fprintf(&b, " (logId %s)", e.LogID)
	}
	return b.String()
}

// ErrZoneNotFound is returned when no zone of the account matches a domain.
var ErrZoneNotFound = errors.New("connbyte: no matching zone found in account")

// ListZones returns all zones of the account.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	var items []struct {
		Zone Zone `json:"zone"`
	}
	if err := c.call(ctx, "/ns/zone/list", struct{}{}, &items); err != nil {
		return nil, err
	}
	zones := make([]Zone, 0, len(items))
	for _, it := range items {
		zones = append(zones, it.Zone)
	}
	return zones, nil
}

// ListRecords returns all records of a zone.
func (c *Client) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	var items []struct {
		Record Record `json:"record"`
	}
	if err := c.call(ctx, "/ns/record/list", map[string]string{"zoneId": zoneID}, &items); err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(items))
	for _, it := range items {
		records = append(records, it.Record)
	}
	return records, nil
}

// CreateRecord creates a record and returns its ID.
func (c *Client) CreateRecord(ctx context.Context, in RecordInput) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.call(ctx, "/ns/record/create", in, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// DeleteRecord deletes a record by ID.
func (c *Client) DeleteRecord(ctx context.Context, zoneID string, recordID int64) error {
	return c.call(ctx, "/ns/record/delete", map[string]any{"zoneId": zoneID, "recordId": recordID}, nil)
}

// FindZone returns the zone that holds fqdn. It prefers the longest zone
// name that is equal to or a parent of fqdn.
func FindZone(zones []Zone, fqdn string) (Zone, error) {
	name := Normalize(fqdn)
	var best Zone
	for _, z := range zones {
		zn := Normalize(z.Zone)
		if zn == "" {
			continue
		}
		if name == zn || strings.HasSuffix(name, "."+zn) {
			if len(zn) > len(Normalize(best.Zone)) {
				best = z
			}
		}
	}
	if best.ID == "" {
		return Zone{}, fmt.Errorf("%w for %q", ErrZoneNotFound, name)
	}
	return best, nil
}

// RelativeName returns the record name of fqdn inside zone. The zone apex
// is returned as an empty string.
func RelativeName(fqdn, zone string) string {
	name, zn := Normalize(fqdn), Normalize(zone)
	if name == zn {
		return ""
	}
	return strings.TrimSuffix(name, "."+zn)
}

// RecordFQDN returns the normalized FQDN of a record. The API returns the
// full name; relative names and "@" are handled as well.
func RecordFQDN(r Record, zone string) string {
	name, zn := Normalize(r.Name), Normalize(zone)
	switch {
	case name == "" || name == "@":
		return zn
	case name == zn || strings.HasSuffix(name, "."+zn):
		return name
	default:
		return name + "." + zn
	}
}

// Normalize lowercases a domain name and removes the trailing dot.
func Normalize(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

// TXTValue returns the content of a TXT record without surrounding quotes.
func TXTValue(content string) string {
	if len(content) >= 2 && strings.HasPrefix(content, `"`) && strings.HasSuffix(content, `"`) {
		if u, err := strconv.Unquote(content); err == nil {
			return u
		}
		return content[1 : len(content)-1]
	}
	return content
}

func (c *Client) baseURL() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) call(ctx context.Context, path string, payload, out any) error {
	if c.Token == "" {
		return errors.New("connbyte: API token is empty")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("connbyte: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("connbyte: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	ua := c.UserAgent
	if ua == "" {
		ua = "cert-manager-webhook-connbyte"
	}
	req.Header.Set("User-Agent", ua)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("connbyte: %s: %w", path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("connbyte: %s: read response: %w", path, err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("connbyte: %s: HTTP %d: decode response: %w", path, resp.StatusCode, err)
	}
	if resp.StatusCode/100 != 2 || (env.Status.Code != 0 && env.Status.Code/100 != 2) {
		code := env.Status.Code
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{
			HTTPStatus: resp.StatusCode,
			Code:       code,
			Title:      env.Status.Title,
			Message:    env.Status.Message,
			LogID:      env.Status.LogID,
			Fields:     env.Field,
		}
	}
	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("connbyte: %s: decode data: %w", path, err)
	}
	return nil
}
