package libp2ptls

import (
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewIdentityCertificates(t *testing.T) {
	_, key := createPeer(t)
	cn := "a.test.name"
	email := "unittest@example.com"

	t.Run("NewIdentity with default template", func(t *testing.T) {
		// Generate an identity using the default template
		id, err := NewIdentity(key)
		assert.NoError(t, err)

		// Extract the x509 certificate
		x509Cert, err := x509.ParseCertificate(id.config.Certificates[0].Certificate[0])
		assert.NoError(t, err)

		// verify the common name and email are not set
		assert.Empty(t, x509Cert.Subject.CommonName)
		assert.Empty(t, x509Cert.EmailAddresses)
	})

	t.Run("NewIdentity with custom template", func(t *testing.T) {
		tmpl, err := certTemplate()
		assert.NoError(t, err)

		tmpl.Subject.CommonName = cn
		tmpl.EmailAddresses = []string{email}

		// Generate an identity using the custom template
		id, err := NewIdentity(key, WithCertTemplate(tmpl))
		assert.NoError(t, err)

		// Extract the x509 certificate
		x509Cert, err := x509.ParseCertificate(id.config.Certificates[0].Certificate[0])
		assert.NoError(t, err)

		// verify the common name and email are set
		assert.Equal(t, cn, x509Cert.Subject.CommonName)
		assert.Equal(t, email, x509Cert.EmailAddresses[0])
	})
}
