package checks

import (
	"context"
	"diffharbor/internal/proc"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRegistries(t *testing.T) {
	t.Setenv("NPM_REGISTRIES", "* https://art.corp/npm/virtual tokAll, * @corp https://art.corp/npm/internal/ tokAll\n"+
		"Acme @corp https://nexus.acme/npm/ tokAcme, other @x https://x.example/ tokX")
	got, err := Registries("acme")
	want := []Registry{{"", "https://art.corp/npm/virtual/", "tokAll"}, {"@corp", "https://nexus.acme/npm/", "tokAcme"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("acme: %v %v, want %v", got, err, want)
	}
	if got, _ := Registries("someone"); len(got) != 2 || got[1].Token != "tokAll" {
		t.Fatalf("someone: %v", got)
	}
	for _, bad := range []string{
		"* art.corp/npm secret", "* corp https://art.corp/ secret", "* https://art.corp/", "* @a https://a/ secret extra",
		"* https://art.corp/npm?t=x secret", "* https://u:p@art.corp/npm secret", "* https://art.corp/npm#x secret",
	} {
		t.Setenv("NPM_REGISTRIES", bad)
		if _, err := Registries(""); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestRegistryLockfile(t *testing.T) {
	defer func(pi bool) { proc.AsPi = pi }(proc.AsPi)
	proc.AsPi = false
	env, urls, err := RegistryEnv(context.Background(), []Registry{{"", "https://art.corp/npm/", "tok"}, {"@corp", "https://art.corp/npm/internal/", "user:pw"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"npm_config_registry=https://art.corp/npm/", "npm_config_//art.corp/npm/:_authToken=tok",
		"npm_config_@corp:registry=https://art.corp/npm/internal/", "npm_config_//art.corp/npm/internal/:_auth=dXNlcjpwdw==",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("no %s in %q", want, env)
		}
	}
	if urls[0] != "https://art.corp/npm/internal/" { // longest first: under the default's path, it keeps its own
		t.Fatalf("order: %q", urls)
	}
	urls[1] = "http://vault/npm/internal-loan/"
	lock := filepath.Join(t.TempDir(), "package-lock.json")
	orig := `"resolved": "https://registry.npmjs.org/a/-/a-1.0.0.tgz", "resolved": "https://art.corp/npm/internal/@corp/b/-/b-1.0.0.tgz"`
	os.WriteFile(lock, []byte(orig), 0o644)
	restore := pointAt(context.Background(), &proc.Sandbox{}, lock, urls)
	if b, _ := os.ReadFile(lock); string(b) != `"resolved": "https://art.corp/npm/a/-/a-1.0.0.tgz", "resolved": "http://vault/npm/internal-loan/@corp/b/-/b-1.0.0.tgz"` {
		t.Fatalf("rewritten: %s", b)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(lock); string(b) != orig {
		t.Fatalf("restored: %s", b)
	}
	// Not a regular file: never read (a FIFO would block, /dev/zero never ends).
	link := filepath.Join(t.TempDir(), "package-lock.json")
	os.Symlink("/dev/zero", link)
	if err := pointAt(context.Background(), &proc.Sandbox{}, link, urls)(); err != nil {
		t.Fatal(err)
	}
}
