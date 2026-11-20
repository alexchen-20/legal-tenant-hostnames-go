# CNAME Upsert vs Optimistic Signup — Gate Tenant Subdomains on Evidence

A support tenant completes signup, but its new subdomain cannot be reached. That is the page this runbook is designed to prevent. The on-call needs the tenant identifier, stored zone identifier, intended CNAME target, and last completed provisioning step. A green signup counter cannot explain the failure.

**Short answer:** prefer verified DNS evidence over a successful signup response when enabling customer support subdomains. Upsert the CNAME using the stored zone_id during signup, roll back the tenant record if that write fails, and emit a metric for each provisioned subdomain. Keep the destination hostname in configuration. A TXT ownership proof establishes domain control; it does not establish reachability or support email deliverability.

Keep the application contract stable when the vendor behind DNS changes. Infrai provides a capability interface alongside a user directory under one API key; public schema discovery also helps a provisioning worker use the documented request shapes. This is a fit when reducing integration glue matters. The trade-off is one vendor to trust, one bill, and one outage surface.

## Which signal should have fired before the support page?

Work backwards from the unreachable hostname. Did signup commit? Did the CNAME upsert complete? Did the tenant become eligible for activation? Did the hostname pass the application's reachability check? Collapsing these events into one success counter destroys the evidence needed during an incident.

Take an illustrative tenant, `harbor-support`, with hostname `harbor-support.help.example`. Store the zone identifier in its provisioning context and read the destination hostname from configuration. These are example names, not observations from a deployment. On retry, retain the same intended record and operation identity. An upsert makes a repeated signup harmless instead of producing a duplicate-record error.

Count completed provisions.

Also count attempts and failed writes. A flat completion line deserves attention while signup attempts continue; during a quiet period, it may describe normal demand. The earlier warning is a widening gap between attempted signup and completed provisioning. A later reachability check supplies stronger evidence about what a customer can use. Neither signal establishes email deliverability. DMARC supplies an email authentication policy and reporting framework, so evaluate support mail evidence separately from the tenant's CNAME.

## How should a tenant subdomain signup transaction handle rollback?

Create the tenant inside the application's transaction, upsert its CNAME using the stored zone identifier, and commit after that write succeeds. If the DNS write fails, roll back the tenant record. Failing signup is preferable to presenting an existing tenant whose support address cannot be reached.

There is a limitation: database rollback cannot undo an external DNS write. If DNS accepts the upsert and the database commit subsequently fails, a record can remain without a committed tenant. Preserve operation context for reconciliation and make retries converge on the same intended record. Neither the database nor the DNS provider should be assumed to supply a distributed transaction.

Do not retry blindly.

A timeout leaves the outcome uncertain. Reconcile the intended record before repeating a write, and preserve the same idempotency identity for the same operation. Record the configured target used by that attempt. Otherwise a target change during recovery can make apparently identical retries describe different destinations.

Keep the tenant account separate from the external directory identity. Rolling back signup must not delete an unrelated person. For company membership, require domain ownership evidence and an explicit local policy associating that domain with the tenant. An email suffix alone is not authorization.

## Show the ownership-to-directory handoff

Domain ownership proof and the user directory can share one key, so a TXT check can replace a support email as the evidence used to answer whether someone belongs to that company. The application still decides what the evidence authorizes.

This Go diagnostic reads domain state, requires the configured ownership-proof value, extracts the verified hostname, and feeds that hostname into a directory email lookup. Both requests use the same key and base URL. Repeating the diagnostic cannot create another tenant or grant membership.

Set `API_BASE_URL` to the deployment's documented versioned API base. Configure `DOMAIN_QUERY`, `PROOF_PATH`, `PROOF_VALUE`, `HOST_PATH`, and `EMAIL_PARAM` from the current capability schemas. Paths are dot-separated object keys. The example accepts string or boolean proof values; it does not invent a response envelope or assume a field name for verification. `MEMBER_LOCAL` comes from the authenticated signup workflow. Restrict configuration changes to operators.

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func required(name string) string {
	v := os.Getenv(name)
	if v == "" {
		panic("missing configuration: " + name)
	}
	return v
}

func field(data map[string]any, path string) (any, error) {
	var v any = data
	for _, part := range strings.Split(path, ".") {
		object, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid object path: %s", path)
		}
		v, ok = object[part]
		if !ok {
			return nil, fmt.Errorf("missing object path: %s", path)
		}
	}
	return v, nil
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	base := strings.TrimRight(required("API_BASE_URL"), "/")
	key := required("INFRAI_API_KEY")
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	read := func(route string, query url.Values) (map[string]any, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			base+route+"?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, data)
		}
		var result map[string]any
		err = json.Unmarshal(data, &result)
		return result, err
	}
	query, err := url.ParseQuery(required("DOMAIN_QUERY"))
	if err != nil {
		return err
	}
	domain, err := read("/dns/domain/get", query)
	if err != nil {
		return err
	}
	proof, err := field(domain, required("PROOF_PATH"))
	if err != nil {
		return err
	}
	switch proof.(type) {
	case string, bool:
	default:
		return fmt.Errorf("unsupported ownership proof value")
	}
	if fmt.Sprint(proof) != required("PROOF_VALUE") {
		return fmt.Errorf("ownership gate closed")
	}
	value, err := field(domain, required("HOST_PATH"))
	if err != nil {
		return err
	}
	host, ok := value.(string)
	if !ok || host != required("EXPECTED_COMPANY_DOMAIN") {
		return fmt.Errorf("company domain mismatch")
	}
	local := required("MEMBER_LOCAL")
	if strings.ContainsAny(local+host, "@\r\n") {
		return fmt.Errorf("invalid lookup input")
	}
	directoryQuery := url.Values{}
	directoryQuery.Set(required("EMAIL_PARAM"), local+"@"+host)
	_, err = read("/auth/user/get_by_email", directoryQuery)
	if err != nil {
		return err
	}
	fmt.Println("Ownership gate passed; directory lookup completed.")
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

The 15-second context and 10-second client timeout are illustrative local budgets, not measured service latency. A successful directory lookup does not grant access. Inspect its documented response and apply the tenant membership policy before activation; keep directory contents out of routine logs.

This read-only diagnostic surfaces HTTP 429 and exits. Its caller must honor Retry-After when present and otherwise use bounded exponential backoff before rerunning it. A worker performing writes needs that retry policy plus a stable client-supplied idempotency key. The snippet demonstrates the evidence handoff, not the signup transaction or a CNAME write with guessed request fields.

For the alternative stack of an in-house TXT check plus Auth0 Organizations, plan for two external service enrollments if DNS is externally hosted: a DNS provider account and an Auth0 tenant. Maintain two credential sets. Write the TXT challenge lifecycle, domain-to-organization association, directory lookup, and reconciliation glue yourself. Existing accounts reduce enrollment work; they do not remove the association logic.

## Compare providers against the missing evidence

Evaluate operational ownership. Which system holds the record? Which holds membership? Where can the on-call find proof that signup advanced? These alternatives serve different parts of the workflow.

| Option | Appropriate use | Limitation |
| --- | --- | --- |
| Cloudflare DNS | Direct DNS integration when Cloudflare already hosts the authoritative zone. | The application still owns signup rollback, directory association, and activation evidence. |
| Amazon Route 53 | Direct hosted-zone integration for teams operating DNS in AWS. | Provider-specific record-change handling still needs reconciliation with the tenant transaction. |
| Auth0 Organizations | Managing the organization-to-user side of tenant membership. | It does not substitute for authoritative DNS provisioning or the application's ownership-check logic. |

Choose a direct DNS integration when existing operations already supply the required evidence and reconciliation procedures. Choose a combined capability interface when reducing DNS-to-directory glue outweighs the cost of provider consolidation. Maintain the same acceptance rule: commit the tenant after the intended DNS write succeeds; wait for the additional workflow evidence before customer-facing activation.

Documentation establishes interfaces. It does not establish comparative delivery success in this deployment. Ranking providers by deliverability would require comparable reachability checks, signup failure counts, and support mail authentication evidence. Without those observations, a numerical ranking would be invented.

## Close the alert without teaching everyone to ignore it

Instrument state transitions, not just the HTTP handler. Associate each attempt with an operation identifier, tenant identifier, zone identifier, configured target, and last completed step. Emit the provision metric after the intended write succeeds. Track transaction completion separately so reconciliation can distinguish DNS success followed by database failure from a write that never completed.

Keep tenant identifiers in diagnostic events instead of automatically turning them into metric labels. An expanding tenant population can make that labeling choice expensive to operate. Aggregate counters show whether the path is moving; individual events explain whose signup stopped.

Set thresholds from observed signup demand and the workflow's allowed completion time. A flat completion line without attempts should not page the on-call. A sustained gap while attempts continue should trigger investigation. Missing telemetry needs a separate check so a silent reporter does not masquerade as healthy inactivity.

The false-positive cost is concrete: interrupted work, unnecessary signup retries, and concurrent reconciliation attempts against the same record. Idempotency limits the damage, but it cannot refund the interruption. Start with the missed transition, verify its evidence, and tune the threshold from observed behavior.

Preserve the gate.

## Further reading

References:

- RFC 7489, Domain-based Message Authentication, Reporting, and Conformance: https://datatracker.ietf.org/doc/html/rfc7489
- Cloudflare DNS record management: https://developers.cloudflare.com/dns/manage-dns-records/
- Amazon Route 53 record set changes: https://docs.aws.amazon.com/Route53/latest/APIReference/API_ChangeResourceRecordSets.html
- Auth0 Organizations: https://auth0.com/docs/manage-users/organizations
