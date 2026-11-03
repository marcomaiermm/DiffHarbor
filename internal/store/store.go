// Package store keeps the reports by slash-separated keys, like "github.com/o/r/7/versions.json".
package store

import (
	"io/fs"
	"os"
	"path"
)

// Store is where the reports live: a folder now, an S3 bucket next to it.
type Store interface {
	Get(key string) ([]byte, error) // fs.ErrNotExist when there is none
	Put(key string, b []byte) error // whole or not at all: a reader never gets half of it
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

// Put writes key through a temp file and a rename, making its folders.
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
	if err := r.WriteFile(key+".tmp", b, 0o600); err != nil {
		return err
	}
	return r.Rename(key+".tmp", key)
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
