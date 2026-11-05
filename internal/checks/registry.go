package checks

import (
	"cmp"
	"context"
	"diffharbor/internal/proc"
	"diffharbor/internal/vault"
	"encoding/base64"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Registry is an npm registry the installs read from: Scope "" the default one, else "@corp".
type Registry struct{ Scope, URL, Token string }

// Registries are NPM_REGISTRIES' for owner (forge.Event.Owner): entries "<owner|*> [@scope] <url> <token>", split by
// commas or newlines; an owner's own entry beats * for its scope. Every entry is checked, "" checks them all.
func Registries(owner string) ([]Registry, error) {
	byScope := map[string]Registry{}
	for i, e := range strings.FieldsFunc(os.Getenv("NPM_REGISTRIES"), func(r rune) bool { return r == ',' || r == '\n' }) {
		f := strings.Fields(e)
		if len(f) == 3 {
			f = slices.Insert(f, 1, "")
		}
		var u *url.URL
		if len(f) == 4 {
			u, _ = url.Parse(f[2])
		}
		if u == nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			f[1] != "" && !strings.HasPrefix(f[1], "@") {
			return nil, fmt.Errorf(`NPM_REGISTRIES entry %d: want "<owner|*> [@scope] <url> <token>"`, i+1) // never the token
		}
		if _, ok := byScope[f[1]]; f[0] == "*" && ok || f[0] != "*" && !strings.EqualFold(f[0], owner) {
			continue
		}
		byScope[f[1]] = Registry{Scope: f[1], URL: strings.TrimSuffix(f[2], "/") + "/", Token: f[3]}
	}
	return slices.SortedFunc(maps.Values(byScope), func(a, b Registry) int { return cmp.Compare(a.Scope, b.Scope) }), nil
}

// RegistryEnv lends regs through the vault until ctx is done and returns the installs' env, and the lockfiles' URLs
// to rewrite (old, new pairs, longest old first: a registry under another's path keeps its own): npm ci fetches a
// lockfile's tarballs where it says, and keeps only the host of the registry it points them at
// (replace-registry-host), which loses the vault's token. Without the vault (not root) the tokens go in the env as
// they are.
func RegistryEnv(ctx context.Context, regs []Registry) (env, urls []string, err error) {
	var pairs [][2]string
	for _, r := range regs {
		via := r.URL
		if vault.URL() != "" {
			if via, err = vault.Npm(ctx, r.URL, r.Token); err != nil {
				return nil, nil, err
			}
		} else {
			auth := "_authToken=" + r.Token
			if strings.Contains(r.Token, ":") {
				auth = "_auth=" + base64.StdEncoding.EncodeToString([]byte(r.Token))
			}
			// //host/path/:_authToken, a key npm takes from the env as it is
			env = append(env, "npm_config_"+strings.TrimPrefix(strings.TrimPrefix(r.URL, "https:"), "http:")+":"+auth)
		}
		pairs = append(pairs, [2]string{r.URL, via})
		if r.Scope != "" {
			env = append(env, "npm_config_"+r.Scope+":registry="+via) // Yarn Berry takes scopes from .yarnrc.yml only
			continue
		}
		env = append(env, "npm_config_registry="+via, "YARN_NPM_REGISTRY_SERVER="+via, "YARN_UNSAFE_HTTP_WHITELIST=127.0.0.1")
		pairs = append(pairs, [2]string{"https://registry.npmjs.org/", via}, [2]string{"https://registry.yarnpkg.com/", via})
	}
	slices.SortStableFunc(pairs, func(a, b [2]string) int { return len(b[0]) - len(a[0]) })
	for _, p := range pairs {
		urls = append(urls, p[0], p[1])
	}
	return env, urls, nil
}

// maxLockfile bounds the lockfile pointAt reads: the PR picks what is there.
const maxLockfile = 64 << 20

// pointAt rewrites lockfile's registry URLs (old, new pairs) and returns what puts the PR's file back for the checks
// after the install. A lockfile that isn't a regular file (a symlink to /dev/zero, a FIFO) or is too big stays.
func pointAt(ctx context.Context, lockfile string, urls []string) (restore func() error) {
	restore = func() error { return nil }
	if fi, err := os.Lstat(lockfile); len(urls) == 0 || err != nil || !fi.Mode().IsRegular() || fi.Size() > maxLockfile {
		return restore
	}
	// the file may change after Lstat: head reads as the repo's user, bounded
	b, err := proc.Run(proc.Command(ctx, "/", nil, "head", "-c", strconv.Itoa(maxLockfile+1), "--", lockfile))
	if err != nil || len(b) > maxLockfile {
		return restore
	}
	orig := string(b)
	nb := strings.NewReplacer(urls...).Replace(orig)
	if nb == orig {
		return restore
	}
	if err := proc.WriteFile(lockfile, []byte(nb)); err != nil {
		_ = proc.WriteFile(lockfile, []byte(orig)) // a failed write may have truncated it
		return restore
	}
	return func() error { return proc.WriteFile(lockfile, []byte(orig)) }
}
