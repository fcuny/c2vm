package image

import (
	"errors"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

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
