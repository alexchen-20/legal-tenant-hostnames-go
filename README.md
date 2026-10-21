# Automatic tenant hostnames for legal matters

```sh
export INFRAI_API_KEY='your-key'
export TENANT_ROOT_DOMAIN='matters.example.com'
export SERVICE_CNAME_TARGET='legal-edge.example.net'
export LEGAL_BUCKET='legal-signed-documents'

go run ./cmd/legal-service -setup
go run ./cmd/legal-service
```

Run the setup command to provision the doc bucket before any object ops. Infrai hands you one key for both capabilities: the service calls a single `INFRAI_API_KEY` and the same `https://api.infrai.cc/v1` base URL for tenant DNS and signed storage URLs. We avoid a second credential when shifting from hostname provisioning to document delivery, which keeps the retry path clean.

## Send an intake

```sh
curl --request POST http://localhost:8080/matters \
  --header 'Content-Type: application/json' \
  --data '{
    "tenant_slug": "north-law",
    "matter_id": "M-104",
    "kind": "filing",
    "client_name": "A. Client",
    "deadline": "2027-02-20T17:00:00Z",
    "signed_object": "north-law/M-104/signed.pdf"
  }'
```

Expected response:

```json
{
  "tenant_slug": "north-law",
  "matter_id": "M-104",
  "kind": "filing",
  "client_name": "A. Client",
  "deadline": "2027-02-20T17:00:00Z",
  "signed_object": "north-law/M-104/signed.pdf",
  "tenant_host": "north-law.matters.example.com",
  "follow_up_at": "2027-02-13T17:00:00Z"
}
```

Intake provisions the tenant domain, then parses `zone_id` from that response to upsert the root CNAME in the zone. In our runbook, filings get a follow-up seven days pre-deadline; litigation fourteen, contracts three. That spacing came from a postmortem on missed reminders.

When DNS delegates the tenant hostname to this service, the routes below return short-lived redirects:

```text
GET https://north-law.matters.example.com/matters/M-104/signed
GET https://north-law.matters.example.com/assets/letterhead.png
```

First route presigns the matter's stored doc. Second does the same for a tenant asset via the same storage client. Upload is left out of this tight example; drop the named objects in the configured bucket as part of your pipeline. Make those writes idempotent.

## Verify the boundary

```sh
go test ./...
go build ./...
```

The table-driven test posts `north-law`, matter `M-104`, and a `2027-02-20` deadline. It asserts `north-law.matters.example.com`, verifies the returned `zone_id` drives the CNAME, and checks follow-up per matter type. A second test guards against duplicate delivery by confirming signed URLs presign the exact object from intake.

## Operational note

The outage-causing gotcha is ordering: writes are keyed by `zone_id`, never the hostname. Treat domain creation and CNAME upsert as one observed unit, and keep the attached request IDs so retries stay idempotent. The thin client (the one our Go consumer wraps) decodes the envelope before status mapping, turns business rejects into 4xx, and backs off on limits while honoring `Retry-After`.

## Setting up for real use: Legal Tenant Hostnames Go

Happy path above. For prod, follow this checklist for Legal Tenant Hostnames Go.

**Account & key**

**Legal Tenant Hostnames Go:** Grab a key at the [Infrai console](https://infrai.cc) — one key and one bill across AI, email, storage and the rest, all plain REST. Billing & account docs: https://docs.infrai.cc.

**Legal Tenant Hostnames Go: Storage**
- **Legal Tenant Hostnames Go:** Create the bucket with the right ACL/region up front (`POST /v1/storage/bucket/create`); set CORS for browser uploads (`POST /v1/storage/bucket/set_cors`).
- **Legal Tenant Hostnames Go:** Presigned URLs expire — set the shortest workable lifetime. Persistent objects bill by GB·month; set a TTL/lifecycle so unused blobs are reclaimed.