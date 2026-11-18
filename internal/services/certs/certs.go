// Package certs issues and renews one wildcard certificate per Cloudflare zone
// (*.zone and zone) from Let's Encrypt over DNS-01. Issuing centrally means a
// server that is replaced or rotated ten times a week costs no new
// certificate: the same one is copied to whichever server holds the name.
package certs

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"gorm.io/gorm"

	"microserver/internal/models"
	"microserver/internal/services/cloudflare"
	"microserver/internal/services/secret"
)

// RenewBefore is how close to expiry a certificate is renewed.
const RenewBefore = 30 * 24 * time.Hour

const StagingURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

// TXTAPI is the part of the Cloudflare client DNS-01 needs.
type TXTAPI interface {
	CreateTXT(ctx context.Context, zoneID, name, value string) (string, error)
	DeleteRecord(ctx context.Context, zoneID, id string) error
}

type Manager struct {
	DB *gorm.DB
	// Directory is the ACME directory URL (empty = Let's Encrypt production).
	Directory func() string
	// DNS returns the record API of a Cloudflare account; replaced in tests.
	DNS func(acc *models.CloudflareAccount) TXTAPI
	// PropagationWait bounds the wait for the TXT record to be visible.
	PropagationWait time.Duration

	mu sync.Mutex
}

func New(db *gorm.DB) *Manager {
	return &Manager{
		DB:              db,
		Directory:       func() string { return acme.LetsEncryptURL },
		DNS:             func(acc *models.CloudflareAccount) TXTAPI { return cloudflare.New(string(acc.Token)) },
		PropagationWait: 2 * time.Minute,
	}
}

// Due reports whether c is missing or close enough to expiry to renew.
func Due(c *models.Certificate) bool {
	return c == nil || c.CertPEM == "" || c.NotAfter == nil || time.Until(*c.NotAfter) < RenewBefore
}

// Ensure returns a valid certificate for the zone, issuing or renewing it
// first when needed. renewed is true when a new one was just issued.
func (m *Manager) Ensure(ctx context.Context, accountID uint, zoneID, zoneName string, logf func(string, ...any)) (cert *models.Certificate, renewed bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if logf == nil {
		logf = func(string, ...any) {}
	}
	zoneName = strings.TrimSuffix(strings.ToLower(zoneName), ".")
	var c models.Certificate
	if err := m.DB.Where("zone_name = ?", zoneName).First(&c).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	if !Due(&c) {
		return &c, false, nil
	}
	var acc models.CloudflareAccount
	if err := m.DB.First(&acc, accountID).Error; err != nil {
		return nil, false, fmt.Errorf("cloudflare account %d: %w", accountID, err)
	}
	c.CFAccountID, c.CFZoneID, c.ZoneName = acc.ID, zoneID, zoneName
	err = m.issue(ctx, &acc, &c, logf)
	if err != nil {
		c.LastError = truncate(err.Error(), 1000)
	} else {
		c.LastError = ""
	}
	if serr := m.DB.Save(&c).Error; serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		// An older certificate that has not expired is still usable.
		if c.CertPEM != "" && c.NotAfter != nil && time.Now().Before(*c.NotAfter) {
			logf("renewing the %s certificate failed (%v); the current one is valid until %s", zoneName, err, c.NotAfter.Format("2006-01-02"))
			return &c, false, nil
		}
		return nil, false, err
	}
	return &c, true, nil
}

func (m *Manager) issue(ctx context.Context, acc *models.CloudflareAccount, c *models.Certificate, logf func(string, ...any)) error {
	accKey, err := loadOrNewKey(string(c.AccountKey))
	if err != nil {
		return err
	}
	if c.AccountKey == "" {
		pemKey, err := encodeKey(accKey)
		if err != nil {
			return err
		}
		c.AccountKey = secret.String(pemKey)
	}
	client := &acme.Client{Key: accKey, DirectoryURL: m.Directory()}
	contact := []string{}
	if acc.AcmeEmail != "" {
		contact = append(contact, "mailto:"+acc.AcmeEmail)
	}
	if _, err := client.Register(ctx, &acme.Account{Contact: contact}, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return fmt.Errorf("acme register: %w", err)
	}

	names := []string{"*." + c.ZoneName, c.ZoneName}
	logf("ordering a certificate for %s", strings.Join(names, ", "))
	order, err := client.AuthorizeOrder(ctx, acme.DomainIDs(names...))
	if err != nil {
		return fmt.Errorf("acme order: %w", err)
	}

	// Both names validate through the same _acme-challenge record name, so
	// every TXT value is published before any challenge is accepted.
	dns := m.DNS(acc)
	type pending struct {
		authzURL string
		chal     *acme.Challenge
		value    string
	}
	var todo []pending
	var created []string
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		for _, id := range created {
			_ = dns.DeleteRecord(cctx, c.CFZoneID, id)
		}
	}()
	for _, u := range order.AuthzURLs {
		z, err := client.GetAuthorization(ctx, u)
		if err != nil {
			return fmt.Errorf("acme authorization: %w", err)
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, ch := range z.Challenges {
			if ch.Type == "dns-01" {
				chal = ch
			}
		}
		if chal == nil {
			return fmt.Errorf("no dns-01 challenge for %s", z.Identifier.Value)
		}
		val, err := client.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return err
		}
		id, err := dns.CreateTXT(ctx, c.CFZoneID, "_acme-challenge."+z.Identifier.Value, val)
		if err != nil {
			return fmt.Errorf("TXT record: %w", err)
		}
		created = append(created, id)
		todo = append(todo, pending{authzURL: u, chal: chal, value: val})
	}
	if len(todo) > 0 {
		var vals []string
		for _, p := range todo {
			vals = append(vals, p.value)
		}
		logf("waiting for the TXT record to be visible")
		m.waitTXT(ctx, "_acme-challenge."+c.ZoneName, vals)
	}
	for _, p := range todo {
		if _, err := client.Accept(ctx, p.chal); err != nil {
			return fmt.Errorf("acme accept: %w", err)
		}
	}
	for _, p := range todo {
		if _, err := client.WaitAuthorization(ctx, p.authzURL); err != nil {
			return fmt.Errorf("acme validation: %w", err)
		}
	}
	if order, err = client.WaitOrder(ctx, order.URI); err != nil {
		return fmt.Errorf("acme order: %w", err)
	}

	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
	}, certKey)
	if err != nil {
		return err
	}
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return fmt.Errorf("acme finalize: %w", err)
	}
	leaf, err := x509.ParseCertificate(der[0])
	if err != nil {
		return err
	}
	var chain strings.Builder
	for _, b := range der {
		_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: b})
	}
	keyPEM, err := encodeKey(certKey)
	if err != nil {
		return err
	}
	now := time.Now()
	na := leaf.NotAfter
	c.CertPEM, c.KeyPEM, c.NotAfter, c.IssuedAt = chain.String(), secret.String(keyPEM), &na, &now
	logf("certificate for %s issued, valid until %s", c.ZoneName, na.Format("2006-01-02"))
	return nil
}

// waitTXT polls public resolvers until every value is visible or the wait
// runs out; Cloudflare usually serves a new record within seconds, so a
// resolver we cannot reach only costs the wait, not the order.
func (m *Manager) waitTXT(ctx context.Context, name string, want []string) {
	deadline := time.Now().Add(m.PropagationWait)
	for _, server := range []string{"1.1.1.1:53", "8.8.8.8:53"} {
		res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, server)
		}}
		for time.Now().Before(deadline) {
			qctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			got, _ := res.LookupTXT(qctx, name)
			cancel()
			if containsAll(got, want) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func containsAll(got, want []string) bool {
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func loadOrNewKey(p string) (crypto.Signer, error) {
	if p == "" {
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	b, _ := pem.Decode([]byte(p))
	if b == nil {
		return nil, errors.New("stored ACME account key is not PEM")
	}
	return x509.ParseECPrivateKey(b.Bytes)
}

func encodeKey(k crypto.Signer) (string, error) {
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("unsupported key type")
	}
	der, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
