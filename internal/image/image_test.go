package image

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func TestTransient(t *testing.T) {
	status := func(code int) error {
		// As Pull returns them, wrapped.
		return fmt.Errorf("failed to pull alpine: %w", &transport.Error{StatusCode: code})
	}
	refused := &url.Error{Op: "Get", URL: "https://registry/v2/", Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	// Real dial errors, as net makes them: with a deadline that's passed,
	// as when the dialer's timeout expires, and with a canceled context.
	dial := func(ctx context.Context) error {
		_, err := (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:1")
		return &url.Error{Op: "Get", URL: "https://registry/v2/", Err: err}
	}
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"rate limited", status(http.StatusTooManyRequests), true},
		{"server error", status(http.StatusInternalServerError), true},
		{"bad gateway", status(http.StatusBadGateway), true},
		{"unknown manifest", status(http.StatusNotFound), false},
		{"unauthorized", status(http.StatusUnauthorized), false},
		{"denied", status(http.StatusForbidden), false},
		{"unreachable", fmt.Errorf("failed to pull alpine: %w", refused), true},
		{"no such host", &url.Error{Op: "Get", URL: "https://registry/v2/", Err: &net.DNSError{Err: "no such host", Name: "registry", IsNotFound: true}}, true},
		{"dial timeout", dial(expired), true},
		// Any failure to talk to the registry falls back, TLS included:
		// what's cached was checked against its digest when it was pulled.
		{"untrusted certificate", &url.Error{Op: "Get", URL: "https://registry/v2/", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}, true},
		{"canceled", &url.Error{Op: "Get", URL: "https://registry/v2/", Err: context.Canceled}, false},
		{"dial canceled", dial(canceled), false},
		{"other", errors.New("invalid reference"), false},
	} {
		if got := Transient(tt.err); got != tt.want {
			t.Errorf("%s: Transient(%v) = %v, want %v", tt.name, tt.err, got, tt.want)
		}
	}
}

type brokenKeychain struct{}

func (brokenKeychain) Resolve(authn.Resource) (authn.Authenticator, error) {
	return nil, errors.New(`exec: "docker-credential-desktop": executable file not found in $PATH`)
}

type fixedKeychain struct{ auth authn.Authenticator }

func (k fixedKeychain) Resolve(authn.Resource) (authn.Authenticator, error) { return k.auth, nil }

func TestAnonymousFallback(t *testing.T) {
	registry, err := name.NewRegistry("ghcr.io")
	if err != nil {
		t.Fatal(err)
	}

	auth, err := anonymousFallback{brokenKeychain{}}.Resolve(registry)
	if err != nil || auth != authn.Anonymous {
		t.Errorf("broken keychain: got %v, %v; want anonymous", auth, err)
	}

	basic := &authn.Basic{Username: "u", Password: "p"}
	auth, err = anonymousFallback{fixedKeychain{basic}}.Resolve(registry)
	if err != nil || auth != basic {
		t.Errorf("working keychain: got %v, %v; want its credentials", auth, err)
	}
}
