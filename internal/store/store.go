// Package store keeps the reports by slash-separated keys, like "github.com/id:5/7/{secret}/versions.json".
package store

import (
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Store is where the reports live: a folder now, an S3 bucket next to it.
type Store interface {
	Get(key string) ([]byte, error)         // fs.ErrNotExist when there is none
	Open(key string) (io.ReadCloser, error) // Get as a stream, for a report to a browser
	Put(key string, b []byte) error         // whole or not at all: a reader never gets half of it
	Delete(key string) error
	DeleteAll(dir string) error // dir and everything under it; none is no error
}

// Disk is a folder. os.Root keeps every key inside it, through ".." and symlinks too.
type Disk string

func (d Disk) open(key string) (*os.Root, error) {
	if !fs.ValidPath(key) {
		return nil, &fs.PathError{Op: "open", Path: key, Err: fs.ErrInvalid}
	}
	return os.OpenRoot(string(d))
}

// Get reads key.
func (d Disk) Get(key string) ([]byte, error) {
	r, err := d.open(key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.ReadFile(key)
}

// Open opens key; the file stays open past the root.
func (d Disk) Open(key string) (io.ReadCloser, error) {
	r, err := d.open(key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Open(key)
}

// Put writes key through a temp file of its own and a rename, making its folders.
func (d Disk) Put(key string, b []byte) error {
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return err
	}
	r, err := d.open(key)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.MkdirAll(path.Dir(key), 0o700); err != nil {
		return err
	}
	tmp := key + "." + rand.Text() + ".tmp" // one per writer: two puts of a key don't write into one file
	if err := r.WriteFile(tmp, b, 0o600); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	if err := r.Rename(tmp, key); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}

// Delete removes key.
func (d Disk) Delete(key string) error {
	r, err := d.open(key)
	if err != nil {
		return err
	}
	defer r.Close()
	return r.Remove(key)
}

// DeleteAll removes dir and everything under it.
func (d Disk) DeleteAll(dir string) error {
	r, err := d.open(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer r.Close()
	return r.RemoveAll(dir)
}

// MoveTo moves every key into s, each deleted here once s has it, then the folder; none is no error.
// A key s has already stays as s has it: after a failed move, the server wrote newer ones there.
// A put that crashed midway left a temp file, it goes with the folder.
func (d Disk) MoveTo(s Store) error {
	err := fs.WalkDir(os.DirFS(string(d)), ".", func(key string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || strings.HasSuffix(key, ".tmp") {
			return err
		}
		_, err = s.Get(key)
		if errors.Is(err, fs.ErrNotExist) {
			var b []byte
			if b, err = d.Get(key); err == nil {
				err = s.Put(key, b)
			}
		}
		if err == nil {
			err = d.Delete(key)
		}
		return err
	})
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.RemoveAll(string(d))
}
