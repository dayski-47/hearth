// Package tlsutil builds mTLS configs from the local CA + leaf certs.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
)

func pool(caPath string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(pem) {
		return nil, errors.New("tlsutil: no CA certs in " + caPath)
	}
	return p, nil
}

// ServerConfig builds a mutual-TLS config that requires and verifies a client
// certificate signed by the local CA.
func ServerConfig(caPath, certPath, keyPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	ca, err := pool(caPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ClientConfig builds a mutual-TLS config that presents a client certificate
// and verifies the server against the local CA under the given serverName.
func ClientConfig(caPath, certPath, keyPath, serverName string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	ca, err := pool(caPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      ca,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
