//go:build conformance

package main

import (
	"os"
	"testing"
	"time"

	acmetest "github.com/cert-manager/cert-manager/test/acme"

	"github.com/SteffenBarthel/cert-manager-webhook-connbyte/internal/mock"
)

// TestRunsSuite runs the cert-manager DNS01 conformance suite against a local
// mock of the ConnByte API. The mock also serves DNS, so the suite can check
// that records appear and disappear.
//
// Set TEST_ZONE_NAME (with a trailing dot) to override the zone. The suite
// needs the envtest binaries; run it with `make conformance`.
func TestRunsSuite(t *testing.T) {
	zone := os.Getenv("TEST_ZONE_NAME")
	if zone == "" {
		zone = "example.com."
	}

	api := mock.New("mock-token", zone)
	defer api.Close()
	baseURL := api.StartHTTP()
	dnsAddr, err := api.StartDNS()
	if err != nil {
		t.Fatal(err)
	}

	solver := newSolver()
	solver.allowedBaseURLs = []string{baseURL}

	fixture := acmetest.NewFixture(solver,
		acmetest.SetResolvedZone(zone),
		acmetest.SetAllowAmbientCredentials(false),
		acmetest.SetManifestPath("testdata/connbyte"),
		acmetest.SetConfig(map[string]any{
			"apiTokenSecretRef": map[string]string{"name": "connbyte-api-token", "key": "token"},
			"baseUrl":           baseURL,
		}),
		acmetest.SetDNSServer(dnsAddr),
		acmetest.SetUseAuthoritative(false),
		acmetest.SetStrict(true),
		acmetest.SetPollInterval(time.Second),
		acmetest.SetPropagationLimit(30*time.Second),
	)
	fixture.RunConformance(t)
}
