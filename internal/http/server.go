package httpserver

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

func generateSelfSignedCert() (tls.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Registry Proxy"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})

	return tls.X509KeyPair(certPEM, keyPEM)
}

func StartServers(logger *logrus.Logger, handler http.Handler) {
	go func() {
		httpServer := &http.Server{
			Addr:    ":8443",
			Handler: handler,
		}
		logger.WithField("port", 8443).Info("Starting HTTP server")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("HTTP server failed")
		}
	}()

	go func() {
		cert, err := generateSelfSignedCert()
		if err != nil {
			logger.WithError(err).Fatal("Failed to generate self-signed certificate")
		}

		httpsServer := &http.Server{
			Addr:    ":9443",
			Handler: handler,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
			},
		}

		logger.WithField("port", 9443).Info("Starting HTTPS server")
		if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("HTTPS server failed")
		}
	}()
}
