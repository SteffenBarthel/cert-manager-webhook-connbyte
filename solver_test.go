package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/SteffenBarthel/cert-manager-webhook-connbyte/internal/mock"
)

const testToken = "mock-token"

func newTestSolver(t *testing.T, zones ...string) (*connbyteSolver, *mock.Server, string) {
	t.Helper()
	api := mock.New(testToken, zones...)
	t.Cleanup(api.Close)
	url := api.StartHTTP()
	s := newSolver()
	s.getSecret = func(_ context.Context, namespace, name, key string) (string, error) {
		if namespace != "team-a" || name != "connbyte" || key != "token" {
			return "", errors.New("secret not found")
		}
		return testToken + "\n", nil
	}
	return s, api, url
}

func challenge(t *testing.T, baseURL, fqdn, key string, extra map[string]any) *v1alpha1.ChallengeRequest {
	t.Helper()
	cfg := map[string]any{
		"apiTokenSecretRef": map[string]string{"name": "connbyte"},
		"baseUrl":           baseURL,
	}
	for k, v := range extra {
		cfg[k] = v
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &v1alpha1.ChallengeRequest{
		ResourceNamespace: "team-a",
		ResolvedFQDN:      fqdn,
		Key:               key,
		Config:            &extapi.JSON{Raw: raw},
	}
}

func TestPresentAndCleanUp(t *testing.T) {
	s, api, url := newTestSolver(t, "example.com", "sub.example.com")
	ch := challenge(t, url, "_acme-challenge.www.sub.example.com.", "key-1", nil)

	if err := s.Present(ch); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if got := api.TXT("_acme-challenge.www.sub.example.com"); !slices.Equal(got, []string{"key-1"}) {
		t.Fatalf("TXT after Present = %v", got)
	}
	if err := s.CleanUp(ch); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	if got := api.TXT("_acme-challenge.www.sub.example.com"); len(got) != 0 {
		t.Fatalf("TXT after CleanUp = %v", got)
	}
}

func TestPresentIsIdempotent(t *testing.T) {
	s, api, url := newTestSolver(t, "example.com")
	ch := challenge(t, url, "_acme-challenge.example.com.", "key-1", nil)
	for range 3 {
		if err := s.Present(ch); err != nil {
			t.Fatalf("Present: %v", err)
		}
	}
	if got := api.TXT("_acme-challenge.example.com"); len(got) != 1 {
		t.Fatalf("expected one TXT record, got %v", got)
	}
	creates := 0
	for _, c := range api.Calls() {
		if c == "/ns/record/create" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("expected 1 create call, got %d", creates)
	}
}

func TestCleanUpKeepsOtherRecords(t *testing.T) {
	s, api, url := newTestSolver(t, "example.com")
	api.AddRecord("example.com", "_acme-challenge.example.com", "TXT", `"other-key"`)
	api.AddRecord("example.com", "example.com", "TXT", `"key-1"`)
	ch := challenge(t, url, "_acme-challenge.example.com.", "key-1", nil)

	if err := s.Present(ch); err != nil {
		t.Fatalf("Present: %v", err)
	}
	if err := s.CleanUp(ch); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	if got := api.TXT("_acme-challenge.example.com"); !slices.Equal(got, []string{"other-key"}) {
		t.Fatalf("TXT on challenge name = %v", got)
	}
	if got := api.TXT("example.com"); !slices.Equal(got, []string{"key-1"}) {
		t.Fatalf("TXT on apex = %v", got)
	}
}

func TestCleanUpWithoutRecordIsNoop(t *testing.T) {
	s, _, url := newTestSolver(t, "example.com")
	if err := s.CleanUp(challenge(t, url, "_acme-challenge.example.com.", "key-1", nil)); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
}

func TestPresentErrors(t *testing.T) {
	s, _, url := newTestSolver(t, "example.com")
	tests := []struct {
		name string
		ch   *v1alpha1.ChallengeRequest
		want string
	}{
		{"unknown zone", challenge(t, url, "_acme-challenge.example.org.", "k", nil), "no matching zone"},
		{"suffix is not a parent zone", challenge(t, url, "_acme-challenge.notexample.com.", "k", nil), "no matching zone"},
		{"ttl too low", challenge(t, url, "_acme-challenge.example.com.", "k", map[string]any{"ttl": 30}), "ttl must be between"},
		{"bad base url", challenge(t, "file:///etc/passwd", "_acme-challenge.example.com.", "k", nil), "baseUrl"},
		{"missing secret", func() *v1alpha1.ChallengeRequest {
			ch := challenge(t, url, "_acme-challenge.example.com.", "k", nil)
			ch.ResourceNamespace = "team-b"
			return ch
		}(), "secret not found"},
		{"missing config", &v1alpha1.ChallengeRequest{ResolvedFQDN: "_acme-challenge.example.com."}, "apiTokenSecretRef is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Present(tt.ch)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Present error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPresentWrongToken(t *testing.T) {
	s, _, url := newTestSolver(t, "example.com")
	s.getSecret = func(context.Context, string, string, string) (string, error) { return "wrong", nil }
	err := s.Present(challenge(t, url, "_acme-challenge.example.com.", "k", nil))
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Present error = %v, want API 401", err)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(&extapi.JSON{Raw: []byte(`{"apiTokenSecretRef":{"name":"connbyte"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APITokenSecretRef.Key != "token" || cfg.TTL != 60 || cfg.BaseURL != "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}
