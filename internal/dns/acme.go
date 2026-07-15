package dns

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"
)

// acmeBackend is the slice of a DNS provider the ACME DNS-01 solver needs.
type acmeBackend interface {
	Zones(ctx context.Context) ([]string, error)
	UpsertRecord(ctx context.Context, zone string, r Record) error
	DeleteRecord(ctx context.Context, zone, name, rtype string) error
}

// ACMEProvider implements lego's challenge.Provider for DNS-01: it writes and
// removes the `_acme-challenge` TXT record used to prove control of a domain
// (required for wildcard certificates). Backed by any DNS Provider (PowerDNS).
type ACMEProvider struct {
	backend acmeBackend
}

// NewACMEProvider builds a DNS-01 solver over the given DNS backend.
func NewACMEProvider(backend acmeBackend) *ACMEProvider {
	return &ACMEProvider{backend: backend}
}

// Present creates the challenge TXT record.
func (a *ACMEProvider) Present(domain, token, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	zone, err := a.zoneFor(info.EffectiveFQDN)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.backend.UpsertRecord(ctx, zone, Record{
		Name: info.EffectiveFQDN, Type: "TXT", TTL: 60,
		Contents: []string{quoteTXT(info.Value)},
	})
}

// CleanUp removes the challenge TXT record.
func (a *ACMEProvider) CleanUp(domain, token, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	zone, err := a.zoneFor(info.EffectiveFQDN)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.backend.DeleteRecord(ctx, zone, info.EffectiveFQDN, "TXT")
}

// zoneFor resolves which existing zone the challenge FQDN belongs to (longest
// matching suffix among the configured zones).
func (a *ACMEProvider) zoneFor(fqdn string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	zones, err := a.backend.Zones(ctx)
	if err != nil {
		return "", err
	}
	target := strings.ToLower(fqdn)
	best := ""
	for _, z := range zones {
		zl := strings.ToLower(z)
		if (target == zl || strings.HasSuffix(target, "."+zl)) && len(zl) > len(best) {
			best = z
		}
	}
	if best == "" {
		return "", fmt.Errorf("dns: no managed zone for %s", fqdn)
	}
	return best, nil
}

// quoteTXT wraps a TXT value in quotes if not already (PowerDNS stores TXT
// content quoted).
func quoteTXT(v string) string {
	if strings.HasPrefix(v, "\"") {
		return v
	}
	return "\"" + v + "\""
}
