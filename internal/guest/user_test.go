package guest

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const passwd = `root:x:0:0:root:/root:/bin/sh
# a comment
nginx:x:101:101:nginx:/var/cache/nginx:/sbin/nologin
app:x:1000:1000::/home/app:/bin/sh
`

const group = `root:x:0:
nginx:x:101:
app:x:1000:
www-data:x:33:nginx,app
docker:x:999:app
`

func fakeRoot(t *testing.T, passwd, group string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"passwd": passwd, "group": group} {
		if content == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(root, "etc", name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLookupUser(t *testing.T) {
	root := fakeRoot(t, passwd, group)

	for _, tc := range []struct {
		spec string
		want Credential
	}{
		{"", Credential{UID: 0, GID: 0, Home: "/root"}},
		{"root", Credential{UID: 0, GID: 0, Home: "/root"}},
		{"nginx", Credential{UID: 101, GID: 101, Groups: []uint32{33}, Home: "/var/cache/nginx"}},
		{"1000", Credential{UID: 1000, GID: 1000, Groups: []uint32{33, 999}, Home: "/home/app"}},
		{"app:docker", Credential{UID: 1000, GID: 999, Groups: []uint32{33}, Home: "/home/app"}},
		{"app:5000", Credential{UID: 1000, GID: 5000, Groups: []uint32{33, 999}, Home: "/home/app"}},
		{"4242", Credential{UID: 4242, GID: 0, Home: "/"}},
		{"4242:4242", Credential{UID: 4242, GID: 4242, Home: "/"}},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			got, err := LookupUser(root, tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			if got.UID != tc.want.UID || got.GID != tc.want.GID || got.Home != tc.want.Home || !slices.Equal(got.Groups, tc.want.Groups) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLookupUserErrors(t *testing.T) {
	root := fakeRoot(t, passwd, group)
	for _, spec := range []string{"nobody", "app:nogroup"} {
		if _, err := LookupUser(root, spec); err == nil {
			t.Errorf("LookupUser(%q): expected an error", spec)
		}
	}
}

func TestLookupUserNoFiles(t *testing.T) {
	root := fakeRoot(t, "", "")

	got, err := LookupUser(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.UID != 0 || got.GID != 0 || got.Home != "/" {
		t.Errorf("got %+v, want root with / as home", got)
	}

	if _, err := LookupUser(root, "65532:65532"); err != nil {
		t.Errorf("numeric IDs should work without /etc/passwd: %v", err)
	}
	if _, err := LookupUser(root, "nonroot"); err == nil {
		t.Error("names can't be resolved without /etc/passwd")
	}
}
