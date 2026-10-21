package legal

import (
	"context"
	"testing"
	"time"

	"example.com/legal-tenant-service/internal/infrai"
)

type fakePlatform struct {
	zoneID        string
	domain        string
	upsertZoneID  string
	presignedPath string
}

func (f *fakePlatform) AddDomain(_ context.Context, domain, _ string) (infrai.Domain, error) {
	f.domain = domain
	return infrai.Domain{ZoneID: f.zoneID}, nil
}

func (f *fakePlatform) UpsertCNAME(_ context.Context, zoneID, _, _, _ string) error {
	f.upsertZoneID = zoneID
	return nil
}

func (f *fakePlatform) PresignGet(_ context.Context, _, key, _ string, _ time.Duration) (infrai.PresignResult, error) {
	f.presignedPath = key
	return infrai.PresignResult{URL: "https://signed.example/document"}, nil
}

func TestIntakeProvisionsTenantAndSchedulesFollowUp(t *testing.T) {
	deadline := time.Date(2027, 2, 20, 17, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		kind       string
		wantNotice time.Time
	}{
		{name: "litigation gets two weeks", kind: "litigation", wantNotice: deadline.AddDate(0, 0, -14)},
		{name: "filing gets one week", kind: "filing", wantNotice: deadline.AddDate(0, 0, -7)},
		{name: "contract gets three days", kind: "contract", wantNotice: deadline.AddDate(0, 0, -3)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			platform := &fakePlatform{zoneID: "zone-42"}
			service := NewService(platform, "matters.example.com", "edge.example.net", "legal-files")
			matter, err := service.Intake(context.Background(), Intake{
				TenantSlug: "north-law", MatterID: "M-104", Kind: tc.kind,
				ClientName: "A. Client", Deadline: deadline, SignedObject: "north-law/M-104/signed.pdf",
			})
			if err != nil {
				t.Fatal(err)
			}
			if matter.TenantHost != "north-law.matters.example.com" {
				t.Fatalf("tenant host = %q", matter.TenantHost)
			}
			if !matter.FollowUpAt.Equal(tc.wantNotice) {
				t.Fatalf("follow up = %s, want %s", matter.FollowUpAt, tc.wantNotice)
			}
			if platform.domain != matter.TenantHost || platform.upsertZoneID != "zone-42" {
				t.Fatalf("provisioning used domain %q and zone %q", platform.domain, platform.upsertZoneID)
			}
		})
	}
}

func TestSignedDownloadUsesMatterObject(t *testing.T) {
	platform := &fakePlatform{zoneID: "zone-7"}
	service := NewService(platform, "matters.example.com", "edge.example.net", "legal-files")
	deadline := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	_, err := service.Intake(context.Background(), Intake{
		TenantSlug: "north-law", MatterID: "M-105", Kind: "filing",
		ClientName: "B. Client", Deadline: deadline, SignedObject: "north-law/M-105/signed.pdf",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.SignedDownload(context.Background(), "north-law", "M-105")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://signed.example/document" || platform.presignedPath != "north-law/M-105/signed.pdf" {
		t.Fatalf("url = %q, object = %q", got, platform.presignedPath)
	}
}
