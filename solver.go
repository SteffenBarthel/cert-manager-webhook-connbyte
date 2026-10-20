package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cert-manager/cert-manager/pkg/acme/webhook"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	extapi "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/SteffenBarthel/cert-manager-webhook-connbyte/internal/connbyte"
)

const (
	solverName      = "connbyte"
	defaultTTL      = 60
	defaultTokenKey = "token"
	requestTimeout  = 60 * time.Second
)

// secretGetter returns the value of a key in a Kubernetes Secret.
type secretGetter func(ctx context.Context, namespace, name, key string) (string, error)

// connbyteSolver solves ACME DNS-01 challenges with the ConnByte DNS API.
type connbyteSolver struct {
	getSecret  secretGetter
	httpClient *http.Client
}

var _ webhook.Solver = (*connbyteSolver)(nil)

// solverConfig is decoded from issuer.spec.acme.solvers[].dns01.webhook.config.
type solverConfig struct {
	// APITokenSecretRef points to the Secret that holds the ConnByte API token.
	// The Secret must be in the namespace of the Issuer, or in the cluster
	// resource namespace of cert-manager for a ClusterIssuer.
	APITokenSecretRef cmmeta.SecretKeySelector `json:"apiTokenSecretRef"`

	// TTL of the TXT record in seconds (60 to 86400). Default: 60.
	TTL int `json:"ttl,omitempty"`

	// BaseURL overrides the API endpoint. Default: https://api.connbyte.com.
	BaseURL string `json:"baseUrl,omitempty"`
}

func newSolver() *connbyteSolver {
	return &connbyteSolver{httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (s *connbyteSolver) Name() string {
	return solverName
}

func (s *connbyteSolver) Initialize(kubeClientConfig *rest.Config, _ <-chan struct{}) error {
	cl, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return fmt.Errorf("create kubernetes client: %w", err)
	}
	s.getSecret = func(ctx context.Context, namespace, name, key string) (string, error) {
		sec, err := cl.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
		}
		v, ok := sec.Data[key]
		if !ok {
			return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
		}
		return string(v), nil
	}
	return nil
}

// Present creates the TXT record. It is idempotent: if a TXT record with the
// same name and value already exists, nothing is created.
func (s *connbyteSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	cfg, client, err := s.setup(ctx, ch)
	if err != nil {
		return err
	}
	zone, records, err := lookup(ctx, client, ch.ResolvedFQDN)
	if err != nil {
		return err
	}
	fqdn := connbyte.Normalize(ch.ResolvedFQDN)
	for _, r := range records {
		if isChallenge(r, zone.Zone, fqdn, ch.Key) {
			return nil
		}
	}
	_, err = client.CreateRecord(ctx, connbyte.RecordInput{
		ZoneID:  zone.ID,
		Name:    connbyte.RelativeName(fqdn, zone.Zone),
		Type:    "TXT",
		Content: ch.Key,
		TTL:     cfg.TTL,
	})
	if err != nil {
		return fmt.Errorf("create TXT record %s: %w", fqdn, err)
	}
	return nil
}

// CleanUp deletes only the TXT records that carry this challenge key. Other
// TXT records with the same name stay untouched.
func (s *connbyteSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	_, client, err := s.setup(ctx, ch)
	if err != nil {
		return err
	}
	zone, records, err := lookup(ctx, client, ch.ResolvedFQDN)
	if err != nil {
		return err
	}
	fqdn := connbyte.Normalize(ch.ResolvedFQDN)
	for _, r := range records {
		if !isChallenge(r, zone.Zone, fqdn, ch.Key) {
			continue
		}
		if err := client.DeleteRecord(ctx, zone.ID, r.ID); err != nil {
			return fmt.Errorf("delete TXT record %s (id %d): %w", fqdn, r.ID, err)
		}
	}
	return nil
}

func (s *connbyteSolver) setup(ctx context.Context, ch *v1alpha1.ChallengeRequest) (solverConfig, *connbyte.Client, error) {
	cfg, err := loadConfig(ch.Config)
	if err != nil {
		return cfg, nil, err
	}
	if s.getSecret == nil {
		return cfg, nil, errors.New("solver is not initialized")
	}
	token, err := s.getSecret(ctx, ch.ResourceNamespace, cfg.APITokenSecretRef.Name, cfg.APITokenSecretRef.Key)
	if err != nil {
		return cfg, nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return cfg, nil, fmt.Errorf("API token in secret %s/%s is empty", ch.ResourceNamespace, cfg.APITokenSecretRef.Name)
	}
	return cfg, &connbyte.Client{BaseURL: cfg.BaseURL, Token: token, HTTPClient: s.httpClient}, nil
}

func lookup(ctx context.Context, client *connbyte.Client, fqdn string) (connbyte.Zone, []connbyte.Record, error) {
	zones, err := client.ListZones(ctx)
	if err != nil {
		return connbyte.Zone{}, nil, fmt.Errorf("list zones: %w", err)
	}
	zone, err := connbyte.FindZone(zones, fqdn)
	if err != nil {
		return connbyte.Zone{}, nil, err
	}
	records, err := client.ListRecords(ctx, zone.ID)
	if err != nil {
		return connbyte.Zone{}, nil, fmt.Errorf("list records of zone %s: %w", zone.Zone, err)
	}
	return zone, records, nil
}

func isChallenge(r connbyte.Record, zone, fqdn, key string) bool {
	return strings.EqualFold(r.Type, "TXT") &&
		connbyte.RecordFQDN(r, zone) == fqdn &&
		connbyte.TXTValue(r.Content) == key
}

func loadConfig(cfgJSON *extapi.JSON) (solverConfig, error) {
	cfg := solverConfig{}
	if cfgJSON == nil {
		return cfg, errors.New("solver config is missing: apiTokenSecretRef is required")
	}
	if err := json.Unmarshal(cfgJSON.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("decode solver config: %w", err)
	}
	if cfg.APITokenSecretRef.Name == "" {
		return cfg, errors.New("solver config: apiTokenSecretRef.name is required")
	}
	if cfg.APITokenSecretRef.Key == "" {
		cfg.APITokenSecretRef.Key = defaultTokenKey
	}
	if cfg.TTL == 0 {
		cfg.TTL = defaultTTL
	}
	if cfg.TTL < connbyte.MinTTL || cfg.TTL > connbyte.MaxTTL {
		return cfg, fmt.Errorf("solver config: ttl must be between %d and %d", connbyte.MinTTL, connbyte.MaxTTL)
	}
	if cfg.BaseURL != "" && !strings.HasPrefix(cfg.BaseURL, "https://") && !strings.HasPrefix(cfg.BaseURL, "http://") {
		return cfg, errors.New("solver config: baseUrl must be an http(s) URL")
	}
	return cfg, nil
}
