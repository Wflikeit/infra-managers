// SPDX-FileCopyrightText: (C) 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

// Package vaultssh signs ephemeral SSH user certificates via Vault's SSH secrets engine.
package vaultssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"

	vault "github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/api/auth/kubernetes"
	"github.com/open-edge-platform/infra-core/inventory/v2/pkg/logging"
	"golang.org/x/crypto/ssh"
)

var zlog = logging.GetLogger("RAPVaultSSH")

const (
	defaultK8sJWTPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultTimeout    = 30 * time.Second
)

// SignerConfig configures Vault SSH certificate signing.
type SignerConfig struct {
	VaultAddress string
	// Mount is the SSH secrets engine mount path (no trailing slash), e.g. "ssh-client-signer".
	Mount string
	// SignRole is the signing role name under the mount (vault write <mount>/sign/<SignRole>).
	SignRole string
	// KubernetesAuthRole is the Vault role for kubernetes auth.
	KubernetesAuthRole string
	// KubernetesJWTPath is the service account token path; empty uses default in-cluster path.
	KubernetesJWTPath string
}

// Signer issues short-lived SSH user certificates using Vault.
type Signer struct {
	cfg    SignerConfig
	client *vault.Client
	mu     sync.Mutex
}

// NewSigner creates a Vault API client (no login until first use).
func NewSigner(cfg SignerConfig) (*Signer, error) {
	if strings.TrimSpace(cfg.VaultAddress) == "" {
		return nil, fmt.Errorf("vault address is required")
	}
	if strings.TrimSpace(cfg.Mount) == "" {
		return nil, fmt.Errorf("vault SSH mount is required")
	}
	if strings.TrimSpace(cfg.SignRole) == "" {
		return nil, fmt.Errorf("vault SSH sign role is required")
	}
	if strings.TrimSpace(cfg.KubernetesAuthRole) == "" {
		return nil, fmt.Errorf("vault kubernetes auth role is required")
	}
	jwtPath := cfg.KubernetesJWTPath
	if jwtPath == "" {
		jwtPath = defaultK8sJWTPath
	}
	cfg.KubernetesJWTPath = jwtPath

	vcfg := vault.DefaultConfig()
	vcfg.Address = cfg.VaultAddress
	vcfg.Timeout = defaultTimeout
	client, err := vault.NewClient(vcfg)
	if err != nil {
		return nil, fmt.Errorf("vault client: %w", err)
	}
	return &Signer{cfg: cfg, client: client}, nil
}

func (s *Signer) login(ctx context.Context) error {
	k8sAuth, err := kubernetes.NewKubernetesAuth(
		s.cfg.KubernetesAuthRole,
		kubernetes.WithServiceAccountTokenPath(s.cfg.KubernetesJWTPath),
	)
	if err != nil {
		return fmt.Errorf("vault kubernetes auth: %w", err)
	}
	authInfo, err := s.client.Auth().Login(ctx, k8sAuth)
	if err != nil {
		return fmt.Errorf("vault login: %w", err)
	}
	if authInfo == nil || authInfo.Auth == nil {
		return fmt.Errorf("vault login: empty auth response")
	}
	return nil
}

// AuthMethods returns ssh.AuthMethod using a fresh ephemeral key and a Vault-signed user certificate.
func (s *Signer) AuthMethods(ctx context.Context, validPrincipal string) ([]ssh.AuthMethod, error) {
	principal := strings.TrimSpace(validPrincipal)
	if principal == "" {
		return nil, fmt.Errorf("valid_principal is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.login(ctx); err != nil {
		return nil, err
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ephemeral ssh key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("ssh signer: %w", err)
	}
	pub := signer.PublicKey()
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))

	mount := strings.Trim(strings.TrimSpace(s.cfg.Mount), "/")
	role := strings.Trim(strings.TrimSpace(s.cfg.SignRole), "/")
	path := fmt.Sprintf("%s/sign/%s", mount, role)
	sec, err := s.client.Logical().WriteWithContext(ctx, path, map[string]interface{}{
		"public_key":       pubLine,
		"valid_principals": principal,
		"cert_type":        "user",
		// Keep the cert lifetime tight regardless of the role's default TTL:
		// it is consumed exactly once per /term request for the SSH handshake
		// and then discarded together with the in-memory key pair. 5m covers
		// dial + auth with generous margin while minimising the leak-and-reuse
		// window.
		"ttl": "5m",
	})
	if err != nil {
		return nil, fmt.Errorf("vault ssh sign %q: %w", path, err)
	}
	if sec == nil || sec.Data == nil {
		return nil, fmt.Errorf("vault ssh sign: empty secret")
	}
	signedKey, _ := sec.Data["signed_key"].(string)
	signedKey = strings.TrimSpace(signedKey)
	if signedKey == "" {
		return nil, fmt.Errorf("vault ssh sign: missing signed_key in response")
	}

	certPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(signedKey + "\n"))
	if err != nil {
		return nil, fmt.Errorf("parse signed_key: %w", err)
	}
	cert, ok := certPub.(*ssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("signed_key is not an SSH certificate")
	}

	certSigner, err := ssh.NewCertSigner(cert, signer)
	if err != nil {
		return nil, fmt.Errorf("ssh cert signer: %w", err)
	}

	logEvt := zlog.Info().
		Str("principal", principal).
		Str("vault_sign_path", path).
		Uint64("cert_serial", cert.Serial)
	if len(cert.ValidPrincipals) > 0 {
		logEvt = logEvt.Strs("cert_principals", cert.ValidPrincipals)
	}
	if cert.KeyId != "" {
		logEvt = logEvt.Str("cert_key_id", cert.KeyId)
	}
	logEvt.Msg("ephemeral SSH user certificate issued via Vault (ed25519 key pair generated in-process; private key not logged)")

	return []ssh.AuthMethod{ssh.PublicKeys(certSigner)}, nil
}
