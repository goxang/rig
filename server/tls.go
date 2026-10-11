package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Certificate loads certFile and keyFile, or, when both are empty, a self-signed certificate kept
// in dir (made on first run for hosts). Clients pin a self-signed certificate by its fingerprint.
func Certificate(certFile, keyFile, dir string, hosts []string) (tls.Certificate, string, error) {
	if certFile == "" && keyFile == "" {
		certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
		if _, err := os.Stat(certFile); os.IsNotExist(err) {
			if err := selfSigned(certFile, keyFile, hosts); err != nil {
				return tls.Certificate{}, "", err
			}
		}
	}
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return c, "", err
	}
	return c, Fingerprint(c.Certificate[0]), nil
}

func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func selfSigned(certFile, keyFile string, hosts []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "rig serve"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(5, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if h, err := os.Hostname(); err == nil {
		hosts = append(hosts, h)
	}
	for _, h := range append(hosts, "localhost", "127.0.0.1", "::1") {
		if ip := net.ParseIP(h); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else if h != "" {
			tpl.DNSNames = append(tpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}
