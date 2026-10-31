package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QwikByte/noryx/internal/master/httpapi"
	"github.com/QwikByte/noryx/internal/master/s3/s3test"
)

// The signature of the example in AWS's documentation of Signature Version 4.
func TestSignature(t *testing.T) {
	c := New(Config{Endpoint: "s3.amazonaws.com", Region: "us-east-1", Bucket: "examplebucket", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}, nil)
	c.now = func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }
	req, err := http.NewRequest(http.MethodGet, c.url("test.txt", nil).String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-9")
	c.sign(req, hash(nil))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization = %s, want %s", got, want)
	}
	// Keys and queries are encoded as they are signed.
	if u := c.url("a b/ü", map[string][]string{"uploadId": {"x+y/="}, "partNumber": {"2"}}); u.String() != "https://examplebucket.s3.amazonaws.com/a%20b/%C3%BC?partNumber=2&uploadId=x%2By%2F%3D" {
		t.Fatalf("url = %s", u)
	}
}

func client(s *s3test.Server, encrypt bool) *Client {
	c := New(Config{Endpoint: s.Endpoint, Region: "us-east-1", Bucket: s.Bucket, AccessKey: "key", SecretKey: "secret", PathStyle: true, Encrypt: encrypt},
		s.Client().Transport)
	c.wait = 0
	return c
}

func TestObjects(t *testing.T) {
	s := s3test.New(t)
	c := client(s, true)
	ctx := t.Context()
	large := make([]byte, 2*MinPartSize+5)
	_, _ = rand.Read(large)
	for _, data := range [][]byte{[]byte("small"), large} {
		n, err := c.Put(ctx, "copies/a.zip", bytes.NewReader(data), int64(len(data)))
		if err != nil || n != int64(len(data)) {
			t.Fatalf("Put = %d, %v", n, err)
		}
		o := s.Objects()["copies/a.zip"]
		if !bytes.Equal(o.Data, data) || o.Header.Get("X-Amz-Server-Side-Encryption") != "AES256" {
			t.Fatalf("stored %d bytes with %v", len(o.Data), o.Header)
		}
		r, err := c.Get(ctx, "copies/a.zip")
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("Get = %d bytes, %v", len(got), err)
		}
	}
	if err := c.Delete(ctx, "copies/a.zip"); err != nil || len(s.Keys()) != 0 {
		t.Fatalf("Delete = %v, left %v", err, s.Keys())
	}
	if _, err := c.Get(ctx, "copies/a.zip"); !NotFound(err) {
		t.Fatalf("Get of a deleted object = %v", err)
	}
	if err := client(s, false).Delete(ctx, "copies/a.zip"); err != nil {
		t.Fatalf("Delete of a deleted object = %v", err)
	}
}

func TestFailures(t *testing.T) {
	s := s3test.New(t)
	c := client(s, false)
	ctx := t.Context()
	data := make([]byte, MinPartSize+1)

	// A part that fails twice is sent again.
	failed := 0
	s.Fail(func(r *http.Request) int {
		if r.URL.Query().Get("partNumber") == "2" && failed < 2 {
			failed++
			return http.StatusServiceUnavailable
		}
		return 0
	})
	if _, err := c.Put(ctx, "a", bytes.NewReader(data), 0); err != nil || failed != 2 {
		t.Fatalf("Put = %v after %d failures", err, failed)
	}

	// An upload that fails is aborted; refusals aren't tried again.
	tries := 0
	s.Fail(func(r *http.Request) int {
		if r.URL.Query().Get("partNumber") == "2" {
			tries++
			return http.StatusForbidden
		}
		return 0
	})
	_, err := c.Put(ctx, "b", bytes.NewReader(data), 0)
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusForbidden || e.Code != "InternalError" || tries != 1 || s.Uploads() != 0 {
		t.Fatalf("Put = %v after %d tries, %d uploads left", err, tries, s.Uploads())
	}
	if _, ok := s.Objects()["b"]; ok {
		t.Fatal("a failed upload left an object")
	}

	// Reading fails: the upload is aborted too.
	s.Fail(nil)
	if _, err := c.Put(ctx, "c", io.MultiReader(bytes.NewReader(data), failingReader{}), 0); err == nil || s.Uploads() != 0 {
		t.Fatalf("Put = %v, %d uploads left", err, s.Uploads())
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the agent went away") }

// Buckets are addressed in the host unless the path is asked for, and redirects elsewhere
// aren't followed.
func TestAddressing(t *testing.T) {
	var hosts []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts = append(hosts, r.Host+r.URL.Path)
		http.Redirect(w, r, "https://elsewhere.example/", http.StatusMovedPermanently)
	}))
	defer srv.Close()
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: transport.TLSClientConfig.RootCAs, ServerName: "example.com"}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	c := New(Config{Endpoint: "example.com", Region: "auto", Bucket: "backups"}, transport)
	err := c.Delete(t.Context(), "copies/a.zip")
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusMovedPermanently || strings.Join(hosts, " ") != "backups.example.com/copies/a.zip" {
		t.Fatalf("Delete = %v, requests %v", err, hosts)
	}
}

// The answers of a storage are shown to administrators as they are, e.g. that it denied access.
var _ httpapi.Public = (*Error)(nil)
