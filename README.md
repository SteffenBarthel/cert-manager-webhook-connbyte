# cert-manager-webhook-connbyte

[cert-manager](https://cert-manager.io/) ACME DNS01 webhook solver for
[ConnByte](https://connbyte.com/) DNS.

The webhook creates and removes the `_acme-challenge` TXT records with the
[ConnByte DNS API](https://api.connbyte.com/). Use it to get wildcard
certificates, or certificates for hosts that are not reachable from the
internet, from Let's Encrypt or any other ACME CA.

## Requirements

- Kubernetes 1.27 or newer
- [cert-manager](https://cert-manager.io/docs/installation/) 1.14 or newer
- Helm 3
- A [ConnByte](https://connbyte.com/) account with the DNS zone and an API token

## Installation

```sh
git clone https://github.com/SteffenBarthel/cert-manager-webhook-connbyte.git
cd cert-manager-webhook-connbyte

helm install connbyte-webhook ./deploy/connbyte-webhook \
  --namespace cert-manager
```

Install the chart in the namespace of cert-manager.

### Container image

The chart uses `ghcr.io/steffenbarthel/cert-manager-webhook-connbyte`. Until
the first release image is published there, or if you want to use your own
registry, build the image yourself:

```sh
docker build -t registry.example.com/cert-manager-webhook-connbyte:0.1.0 .
helm install connbyte-webhook ./deploy/connbyte-webhook \
  --namespace cert-manager \
  --set image.repository=registry.example.com/cert-manager-webhook-connbyte \
  --set image.tag=0.1.0
```

## Configuration

### 1. Store the API token

Create the token in your ConnByte account and store it in a Secret:

```sh
kubectl create secret generic connbyte-api-token \
  --namespace cert-manager \
  --from-literal=token=<your-connbyte-api-token>
```

Where the Secret must live:

- **ClusterIssuer:** in the cluster resource namespace of cert-manager
  (default `cert-manager`).
- **Issuer:** in the namespace of the Issuer.

By default the webhook may only read Secrets in the namespace of cert-manager.
For namespaced Issuers, list their namespaces when you install the chart:

```sh
helm upgrade --install connbyte-webhook ./deploy/connbyte-webhook \
  --namespace cert-manager \
  --set 'secretAccess.namespaces={cert-manager,team-a}' \
  --set 'secretAccess.resourceNames={connbyte-api-token}'
```

`secretAccess.clusterWide=true` allows reading Secrets in all namespaces. Use
it only if you need it.

Anyone who may create an Issuer in a listed namespace can make the webhook
read a Secret there. Keep `secretAccess.resourceNames` set to the token
Secret. The webhook sends the token only to `https://api.connbyte.com`, or to
an endpoint the operator listed in `allowedBaseUrls`.

### 2. Create the issuer

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-connbyte
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@example.com
    privateKeySecretRef:
      name: letsencrypt-connbyte-account
    solvers:
      - dns01:
          webhook:
            groupName: acme.connbyte.com
            solverName: connbyte
            config:
              apiTokenSecretRef:
                name: connbyte-api-token
                key: token
```

Test with the Let's Encrypt staging server
(`https://acme-staging-v02.api.letsencrypt.org/directory`) first.

### 3. Request a certificate

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: example-com
  namespace: default
spec:
  secretName: example-com-tls
  issuerRef:
    name: letsencrypt-connbyte
    kind: ClusterIssuer
  dnsNames:
    - example.com
    - "*.example.com"
```

### Solver options

| Field | Required | Default | Description |
| --- | --- | --- | --- |
| `apiTokenSecretRef.name` | yes | | Name of the Secret with the API token. |
| `apiTokenSecretRef.key` | no | `token` | Key in the Secret. |
| `ttl` | no | `60` | TTL of the TXT record in seconds (60 to 86400). |
| `baseUrl` | no | `https://api.connbyte.com` | API endpoint. Rejected unless listed in the chart value `allowedBaseUrls`. Only for tests. |

### Chart values

| Value | Default | Description |
| --- | --- | --- |
| `groupName` | `acme.connbyte.com` | API group, used as `groupName` in the issuer. |
| `certManager.namespace` | `cert-manager` | Namespace of cert-manager. |
| `certManager.serviceAccountName` | `cert-manager` | Service account of cert-manager. |
| `secretAccess.namespaces` | `[]` (= `certManager.namespace`) | Namespaces in which the webhook may read Secrets. |
| `secretAccess.resourceNames` | `[]` (all) | Limit Secret access to these names. Recommended when you add namespaces. |
| `secretAccess.clusterWide` | `false` | Allow reading Secrets in all namespaces. |
| `allowedBaseUrls` | `[]` | API endpoints an Issuer may set as `baseUrl` (exact match). Empty: `baseUrl` is rejected. |
| `resources` | 10m/32Mi requests, 100m/128Mi limits | Pod resources. |
| `image.repository` | `ghcr.io/steffenbarthel/cert-manager-webhook-connbyte` | Container image. |
| `image.tag` | chart `appVersion` | Image tag. |

## How it works

- The webhook finds the ConnByte zone for the challenge name. If you have
  `example.com` and `sub.example.com`, a challenge for `www.sub.example.com`
  uses `sub.example.com`.
- `Present` creates a TXT record. If the same record already exists, it
  creates nothing, so retries are safe.
- `CleanUp` deletes only the TXT records with the key of this challenge.
  Other TXT records with the same name stay.

## Development

```sh
make unit          # go vet and unit tests against a mock of the ConnByte API
make conformance   # cert-manager DNS01 conformance suite (downloads envtest)
make rendered-manifest.yaml   # render the Helm chart to _out/
```

The conformance suite runs against an in-memory mock of the ConnByte API with
its own DNS server, so it needs no account, token or network access. Set
`TEST_ZONE_NAME` (for example `example.org.`) to test another zone name.

## License

Apache License 2.0, see [LICENSE](LICENSE). Based on
[cert-manager/webhook-example](https://github.com/cert-manager/webhook-example).

ConnByte: <https://connbyte.com/>
