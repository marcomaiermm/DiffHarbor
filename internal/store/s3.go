package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// S3 is a bucket of an S3-compatible store (SeaweedFS, MinIO, Garage, R2, AWS): path-style, signed with SigV4.
type S3 struct {
	endpoint, bucket, prefix string // "https://host:port", "reports", "" or "diffharbor/"
	key, secret, region      string
}

var client = &http.Client{Timeout: time.Minute}

// NewS3 is the bucket at u, "https://host[:port]/bucket[/prefix]".
func NewS3(u, key, secret, region string) (*S3, error) {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
		return nil, fmt.Errorf("REPORT_S3_URL: want https://host/bucket[/prefix], got %q", u)
	}
	bucket, prefix, _ := strings.Cut(strings.Trim(p.Path, "/"), "/")
	if bucket == "" || key == "" || secret == "" {
		return nil, errors.New("REPORT_S3_URL needs a bucket, REPORT_S3_ACCESS_KEY and REPORT_S3_SECRET_KEY")
	}
	if prefix != "" {
		prefix += "/"
	}
	return &S3{p.Scheme + "://" + p.Host, bucket, prefix, key, secret, region}, nil
}

// Get reads key.
func (s *S3) Get(key string) ([]byte, error) {
	if !fs.ValidPath(key) {
		return nil, &fs.PathError{Op: "get", Path: key, Err: fs.ErrInvalid}
	}
	return s.do(http.MethodGet, s.prefix+key, nil, nil)
}

// Put writes key; S3 swaps an object whole.
func (s *S3) Put(key string, b []byte) error {
	if !fs.ValidPath(key) {
		return &fs.PathError{Op: "put", Path: key, Err: fs.ErrInvalid}
	}
	_, err := s.do(http.MethodPut, s.prefix+key, nil, b)
	return err
}

// Delete removes key.
func (s *S3) Delete(key string) error {
	if !fs.ValidPath(key) {
		return &fs.PathError{Op: "delete", Path: key, Err: fs.ErrInvalid}
	}
	_, err := s.do(http.MethodDelete, s.prefix+key, nil, nil)
	return err
}

// DeleteAll removes everything under dir/. It lists from the start until nothing is left: no continuation token.
func (s *S3) DeleteAll(dir string) error {
	if !fs.ValidPath(dir) {
		return &fs.PathError{Op: "delete", Path: dir, Err: fs.ErrInvalid}
	}
	for {
		b, err := s.do(http.MethodGet, "", url.Values{"list-type": {"2"}, "prefix": {s.prefix + dir + "/"}}, nil)
		if errors.Is(err, fs.ErrNotExist) { // no bucket yet: SeaweedFS makes it on the first put
			return nil
		}
		if err != nil {
			return err
		}
		var l struct{ Contents []struct{ Key string } }
		if err := xml.Unmarshal(b, &l); err != nil { //nolint:gosec // the operator's bucket answers
			return fmt.Errorf("s3 list: %w", err)
		}
		if len(l.Contents) == 0 {
			return nil
		}
		for _, c := range l.Contents {
			if _, err := s.do(http.MethodDelete, c.Key, nil, nil); err != nil {
				return err
			}
		}
	}
}

// do sends a signed request on the object (or, "", the bucket) and returns the body of a 2xx.
func (s *S3) do(method, object string, q url.Values, body []byte) ([]byte, error) {
	path := "/" + escape(s.bucket, false) + "/" + escape(object, true)
	//nolint:gosec // the operator's REPORT_S3_URL, not a request's
	req, err := http.NewRequestWithContext(context.Background(), method, s.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = canonicalQuery(q)
	s.sign(req, body, time.Now())
	res, err := client.Do(req) //nolint:gosec // as above
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	switch {
	case err != nil:
		return nil, err
	case res.StatusCode == http.StatusNotFound && method == http.MethodGet: // no object, or no bucket
		return nil, &fs.PathError{Op: "get", Path: s.bucket + "/" + object, Err: fs.ErrNotExist}
	case res.StatusCode/100 != 2:
		return nil, fmt.Errorf("s3 %s %s: %s: %.300s", method, object, res.Status, b)
	}
	return b, nil
}

// sign adds SigV4's headers to req: host, the payload's hash and the time, signed for service s3.
func (s *S3) sign(req *http.Request, body []byte, t time.Time) {
	sum := sha256.Sum256(body)
	payload, amzDate, day := hex.EncodeToString(sum[:]), t.UTC().Format("20060102T150405Z"), t.UTC().Format("20060102")
	req.Header.Set("X-Amz-Content-Sha256", payload)
	req.Header.Set("X-Amz-Date", amzDate)
	const signed = "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		req.Method, req.URL.EscapedPath(), req.URL.RawQuery,
		"host:" + req.URL.Host + "\nx-amz-content-sha256:" + payload + "\nx-amz-date:" + amzDate + "\n",
		signed, payload,
	}, "\n")
	scope := day + "/" + s.region + "/s3/aws4_request"
	h := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(h[:])
	k := []byte("AWS4" + s.secret)
	for _, part := range []string{day, s.region, "s3", "aws4_request", toSign} {
		m := hmac.New(sha256.New, k)
		m.Write([]byte(part))
		k = m.Sum(nil)
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.key+"/"+scope+", SignedHeaders="+signed+
		", Signature="+hex.EncodeToString(k))
}

// canonicalQuery is q sorted by key, escaped as SigV4 wants: what is sent is what is signed.
func canonicalQuery(q url.Values) string {
	ks := make([]string, 0, len(q))
	for k := range q {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	var ps []string
	for _, k := range ks {
		for _, v := range q[k] {
			ps = append(ps, escape(k, false)+"="+escape(v, false))
		}
	}
	return strings.Join(ps, "&")
}

// escape percent-encodes all but A-Z a-z 0-9 - . _ ~, and / when slash: SigV4's URI encoding.
func escape(s string, slash bool) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', strings.IndexByte("-._~", c) >= 0,
			c == '/' && slash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
