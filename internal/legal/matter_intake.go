package legal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"example.com/legal-tenant-service/internal/infrai"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Platform interface {
	AddDomain(context.Context, string, string) (infrai.Domain, error)
	UpsertCNAME(context.Context, string, string, string, string) error
	PresignGet(context.Context, string, string, string, time.Duration) (infrai.PresignResult, error)
}

type Intake struct {
	TenantSlug   string    `json:"tenant_slug"`
	MatterID     string    `json:"matter_id"`
	Kind         string    `json:"kind"`
	ClientName   string    `json:"client_name"`
	Deadline     time.Time `json:"deadline"`
	SignedObject string    `json:"signed_object"`
}

type Matter struct {
	Intake
	TenantHost string    `json:"tenant_host"`
	FollowUpAt time.Time `json:"follow_up_at"`
}

type Service struct {
	platform    Platform
	rootDomain  string
	serviceHost string
	bucket      string
	mu          sync.RWMutex
	matters     map[string]Matter
}

func NewService(platform Platform, rootDomain, serviceHost, bucket string) *Service {
	return &Service{platform: platform, rootDomain: rootDomain, serviceHost: serviceHost, bucket: bucket, matters: make(map[string]Matter)}
}

func (s *Service) Intake(ctx context.Context, input Intake) (Matter, error) {
	if !slugPattern.MatchString(input.TenantSlug) || input.MatterID == "" || input.Deadline.IsZero() {
		return Matter{}, fmt.Errorf("tenant_slug, matter_id, and deadline are required")
	}
	host := input.TenantSlug + "." + s.rootDomain
	requestID := stableID(input.TenantSlug, input.MatterID)
	domain, err := s.platform.AddDomain(ctx, host, requestID+"-domain")
	if err != nil {
		return Matter{}, err
	}
	if domain.ZoneID == "" {
		return Matter{}, fmt.Errorf("domain response omitted zone_id")
	}
	if err := s.platform.UpsertCNAME(ctx, domain.ZoneID, "@", s.serviceHost, requestID+"-cname"); err != nil {
		return Matter{}, err
	}

	matter := Matter{Intake: input, TenantHost: host, FollowUpAt: FollowUpTime(input.Kind, input.Deadline)}
	s.mu.Lock()
	s.matters[matterKey(input.TenantSlug, input.MatterID)] = matter
	s.mu.Unlock()
	return matter, nil
}

func FollowUpTime(kind string, deadline time.Time) time.Time {
	days := 3
	switch strings.ToLower(kind) {
	case "litigation":
		days = 14
	case "filing":
		days = 7
	case "contract":
		days = 3
	}
	return deadline.AddDate(0, 0, -days)
}

func (s *Service) SignedDownload(ctx context.Context, tenantSlug, matterID string) (string, error) {
	s.mu.RLock()
	matter, ok := s.matters[matterKey(tenantSlug, matterID)]
	s.mu.RUnlock()
	if !ok || matter.SignedObject == "" {
		return "", fmt.Errorf("signed document not found")
	}
	result, err := s.platform.PresignGet(ctx, s.bucket, matter.SignedObject, stableID(tenantSlug, matterID)+"-signed", 10*time.Minute)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

func (s *Service) TenantAsset(ctx context.Context, tenantSlug, objectKey string) (string, error) {
	if !slugPattern.MatchString(tenantSlug) || objectKey == "" {
		return "", fmt.Errorf("tenant and asset are required")
	}
	result, err := s.platform.PresignGet(ctx, s.bucket, objectKey, stableID(tenantSlug, objectKey)+"-asset", 5*time.Minute)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

func matterKey(tenantSlug, matterID string) string { return tenantSlug + "/" + matterID }

func stableID(first, second string) string {
	sum := sha256.Sum256([]byte(first + ":" + second))
	return hex.EncodeToString(sum[:12])
}
