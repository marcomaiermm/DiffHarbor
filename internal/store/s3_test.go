package store

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The signatures of AWS's examples for SigV4 with S3 (List Objects, GET Bucket Lifecycle).
func TestSign(t *testing.T) {
	s := &S3{key: "AKIAIOSFODNN7EXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", region: "us-east-1"}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	for q, want := range map[string]string{
		"?max-keys=2&prefix=J": "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		"?lifecycle=":          "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
	} {
		req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/"+q, nil)
		s.sign(req, nil, at)
		if a := req.Header.Get("Authorization"); !strings.HasSuffix(a, "Signature="+want) {
			t.Error(q, a)
		}
	}
	if got := escape("a/b c+d~é", true); got != "a/b%20c%2Bd~%C3%A9" {
		t.Fatal(got)
	}
}

// fakeS3 is a bucket "b" in memory, like S3 where it counts: it checks every request's signature against what it
// got, and lists 1000 keys at most.
func fakeS3(t *testing.T) string {
	signer := &S3{key: "k", secret: "s", region: "us-east-1"}
	var mu sync.Mutex
	objs := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		at, _ := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
		want := r.Clone(context.Background())
		want.URL.Host = r.Host
		signer.sign(want, body, at)
		if r.Header.Get("Authorization") != want.Header.Get("Authorization") {
			http.Error(w, "SignatureDoesNotMatch", http.StatusForbidden)
			return
		}
		bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case bucket != "b":
			http.Error(w, "NoSuchBucket", http.StatusNotFound)
		case r.Method == http.MethodGet && key == "":
			var l struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []struct{ Key string }
			}
			for _, k := range slices.Sorted(maps.Keys(objs)) {
				if strings.HasPrefix(k, r.URL.Query().Get("prefix")) && len(l.Contents) < 1000 {
					l.Contents = append(l.Contents, struct{ Key string }{k})
				}
			}
			_ = xml.NewEncoder(w).Encode(l)
		case r.Method == http.MethodGet:
			if b, ok := objs[key]; ok {
				w.Write(b)
			} else {
				http.Error(w, "NoSuchKey", http.StatusNotFound)
			}
		case r.Method == http.MethodPut:
			objs[key] = body
		case r.Method == http.MethodDelete:
			delete(objs, key)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/b/pfx"
}

// Against fakeS3, or a real store: S3_TEST_URL=http://localhost:8333/bucket/prefix S3_TEST_KEY=… S3_TEST_SECRET=… go test.
func TestS3(t *testing.T) {
	u, key, secret := os.Getenv("S3_TEST_URL"), os.Getenv("S3_TEST_KEY"), os.Getenv("S3_TEST_SECRET")
	if u == "" {
		u, key, secret = fakeS3(t), "k", "s"
	}
	s, err := NewS3(u, key, secret, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	run := fmt.Sprintf("test-%d", time.Now().UnixNano())
	none, _ := NewS3(s.endpoint+"/none-"+run, key, secret, "us-east-1")
	if err := none.DeleteAll("a"); err != nil {
		t.Fatal("no bucket is nothing to delete:", err)
	}
	if _, err := s.Get(run + "/a.html"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("before any put:", err)
	}
	if err := s.Put(run+"/a.html", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if b, err := s.Get(run + "/a.html"); err != nil || string(b) != "x" {
		t.Fatal(string(b), err)
	}
	bad, _ := NewS3(u, key, "wrong", "us-east-1")
	if _, err := bad.Get(run + "/a.html"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a wrong secret got through:", err)
	}
	if err := s.Delete(run + "/a.html"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(run + "/a.html"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("after delete:", err)
	}
	if err := s.Put("../x", nil); err == nil {
		t.Fatal("put ../x")
	}

	// More than a list's 1000 keys, and a sibling whose name starts like the folder.
	for i := range 1001 {
		if err := s.Put(fmt.Sprintf("%s/7/%04d.html", run, i), nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Put(run+"/70/a.html", []byte("kept"))
	if err := s.DeleteAll(run + "/7"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"/7/0000.html", "/7/1000.html"} { // the first list's and the second's
		if _, err := s.Get(run + k); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("kept under 7:", k, err)
		}
	}
	if b, err := s.Get(run + "/70/a.html"); err != nil || string(b) != "kept" {
		t.Fatal("70 isn't under 7:", err)
	}
	if err := s.DeleteAll(run); err != nil {
		t.Fatal(err)
	}
}
