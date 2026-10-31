// Package s3 is a client for S3-compatible storage, such as AWS S3, Backblaze B2, MinIO or
// Cloudflare R2: it uploads objects of any size, in parts if they are large, downloads and
// deletes them. Requests are signed with AWS Signature Version 4, including the hash of each
// body. It only connects to the configured endpoint, over HTTPS, and follows no redirects.
package s3

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
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// MinPartSize is the size of the parts of large objects, unless they need larger ones.
	MinPartSize = 16 << 20
	maxParts    = 10000
	maxAnswer   = 64 << 10 // bytes read of an answer other than an object
	attempts    = 3        // of a request that failed on the way or that the storage couldn't serve
)

// Config describes a bucket and how to reach it.
type Config struct {
	// Endpoint is the host, with a port unless it is 443, e.g. s3.eu-central-1.amazonaws.com.
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	// PathStyle addresses the bucket in the path, https://endpoint/bucket/key, rather than in
	// the host, https://bucket.endpoint/key, e.g. for MinIO.
	PathStyle bool
	// Encrypt asks the storage to encrypt the objects it keeps with its own keys (SSE-S3).
	Encrypt bool
}

// Client uploads, downloads and deletes the objects of a bucket.
type Client struct {
	cfg  Config
	http *http.Client
	now  func() time.Time
	wait time.Duration // before the first retry, doubled for the next
}

// New returns a client of a bucket that sends its requests through transport, or Go's default
// transport if it is nil.
func New(cfg Config, transport http.RoundTripper) *Client {
	return &Client{cfg: cfg, now: time.Now, wait: time.Second, http: &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Error is an answer of the storage that isn't a success.
type Error struct {
	Status        int
	Code, Message string
}

// errorBody is the XML of an Error.
type errorBody struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func (b errorBody) err(status int) *Error { return &Error{status, b.Code, b.Message} }

// Public tells the master's API that the answer of a storage may be shown as it is, e.g. that it
// denied access, which helps an administrator to set up the storage.
func (*Error) Public() {}

func (e *Error) Error() string {
	msg := "the storage answered " + strconv.Itoa(e.Status) + " " + http.StatusText(e.Status)
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Put uploads what r reads as the object key and returns its size. Objects larger than a part
// are uploaded in parts, which are sized for an object of about sizeHint bytes; the upload is
// aborted if it fails.
func (c *Client) Put(ctx context.Context, key string, r io.Reader, sizeHint int64) (size int64, err error) {
	// The object may become a little larger than the hint, e.g. a backup with secrets hidden.
	partSize := int64(MinPartSize)
	if need := sizeHint / (maxParts * 9 / 10); need >= partSize {
		partSize = (need>>20 + 1) << 20
	}
	part := make([]byte, partSize)
	n, err := io.ReadFull(r, part)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) { // all in one request
		_, err = c.send(ctx, http.MethodPut, key, nil, part[:n], c.encryption())
		return int64(n), err
	}
	if err != nil {
		return 0, err
	}
	var created struct {
		UploadID string `xml:"UploadId"`
	}
	if err := c.call(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil, c.encryption(), &created); err != nil {
		return 0, err
	}
	upload := url.Values{"uploadId": {created.UploadID}}
	defer func() {
		if err != nil {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			_, abortErr := c.send(ctx, http.MethodDelete, key, upload, nil, nil)
			err = errors.Join(err, abortErr)
		}
	}()
	type completed struct {
		Number int    `xml:"PartNumber"`
		ETag   string `xml:"ETag"`
	}
	var parts []completed
	for n > 0 {
		if len(parts) == maxParts {
			return size, fmt.Errorf("the object is larger than %d parts of %d MiB", maxParts, partSize>>20)
		}
		query := url.Values{"partNumber": {strconv.Itoa(len(parts) + 1)}, "uploadId": upload["uploadId"]}
		header, err := c.send(ctx, http.MethodPut, key, query, part[:n], nil)
		if err != nil {
			return size, err
		}
		parts, size = append(parts, completed{len(parts) + 1, header.Get("ETag")}), size+int64(n)
		if n, err = io.ReadFull(r, part); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return size, err
		}
	}
	body, err := xml.Marshal(struct {
		XMLName xml.Name    `xml:"CompleteMultipartUpload"`
		Parts   []completed `xml:"Part"`
	}{Parts: parts})
	if err == nil {
		// The storage may answer 200 and tell in the body that completing failed.
		err = c.call(ctx, http.MethodPost, key, upload, body, nil, &struct{}{})
	}
	return size, err
}

// Get downloads an object, which the caller closes.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	res, err := c.do(ctx, http.MethodGet, key, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

// Delete deletes an object; one that doesn't exist counts as deleted.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.send(ctx, http.MethodDelete, key, nil, nil, nil)
	if e := (*Error)(nil); errors.As(err, &e) && e.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// NotFound reports whether err tells that an object doesn't exist.
func NotFound(err error) bool {
	e := (*Error)(nil)
	return errors.As(err, &e) && (e.Status == http.StatusNotFound || e.Code == "NoSuchKey")
}

func (c *Client) encryption() http.Header {
	if !c.cfg.Encrypt {
		return nil
	}
	return http.Header{"X-Amz-Server-Side-Encryption": {"AES256"}}
}

// send sends a request whose answer has no body that matters, and returns its headers.
func (c *Client) send(ctx context.Context, method, key string, query url.Values, body []byte, header http.Header) (http.Header, error) {
	res, err := c.do(ctx, method, key, query, body, header)
	if err != nil {
		return nil, err
	}
	res.Body.Close()
	return res.Header, nil
}

// call sends a request and decodes the XML of its answer into out, unless it is an error.
func (c *Client) call(ctx context.Context, method, key string, query url.Values, body []byte, header http.Header, out any) error {
	res, err := c.do(ctx, method, key, query, body, header)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxAnswer))
	if err != nil {
		return err
	}
	var e errorBody
	if xml.Unmarshal(data, &e) == nil {
		return e.err(res.StatusCode)
	}
	if err := xml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("the storage answered something else than expected: %w", err)
	}
	return nil
}

// do sends a signed request about an object and returns the answer if it is a success. It
// tries again a few times if the request fails on the way or the storage can't serve it.
func (c *Client) do(ctx context.Context, method, key string, query url.Values, body []byte, header http.Header) (*http.Response, error) {
	u := c.url(key, query)
	wait := c.wait
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		c.sign(req, hash(body))
		res, err := c.http.Do(req)
		if err == nil && res.StatusCode < 300 {
			return res, nil
		}
		if err == nil {
			err = answer(res)
		}
		var e *Error
		retry := ctx.Err() == nil && (!errors.As(err, &e) || e.Status >= 500 || e.Status == http.StatusTooManyRequests)
		if !retry || attempt == attempts {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
			wait *= 2
		}
	}
}

// answer returns the error that a response tells, and closes it.
func answer(res *http.Response) error {
	defer res.Body.Close()
	var e errorBody
	if data, err := io.ReadAll(io.LimitReader(res.Body, maxAnswer)); err == nil {
		_ = xml.Unmarshal(data, &e) // without a body, e.g. for HEAD, the status tells
	}
	return e.err(res.StatusCode)
}

// url returns the URL of an object, with the query in the canonical form that is signed.
func (c *Client) url(key string, query url.Values) *url.URL {
	u := &url.URL{Scheme: "https", Host: c.cfg.Bucket + "." + c.cfg.Endpoint, Path: "/" + key}
	if c.cfg.PathStyle {
		u.Host, u.Path = c.cfg.Endpoint, "/"+c.cfg.Bucket+"/"+key
	}
	u.RawPath = encode(u.Path, false)
	var pairs []string
	for _, k := range slices.Sorted(maps.Keys(query)) {
		for _, v := range query[k] {
			pairs = append(pairs, encode(k, true)+"="+encode(v, true))
		}
	}
	u.RawQuery = strings.Join(pairs, "&")
	return u
}

// encode escapes all but the unreserved characters of RFC 3986, and slashes unless asked to.
func encode(s string, slashes bool) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '.', c == '_', c == '~', c == '/' && !slashes:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// signed are the headers that are signed besides the host and those of Amazon (x-amz-*).
var signed = []string{"content-md5", "content-type", "range"}

// sign adds the date, the hash of the body and the signature (AWS Signature Version 4) to a
// request.
func (c *Client) sign(req *http.Request, payload string) {
	now := c.now().UTC()
	stamp, day := now.Format("20060102T150405Z"), now.Format("20060102")
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", payload)
	names := []string{"host"}
	for k := range req.Header {
		if k = strings.ToLower(k); strings.HasPrefix(k, "x-amz-") || slices.Contains(signed, k) {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	var headers strings.Builder
	for _, k := range names {
		v := req.URL.Host
		if k != "host" {
			v = strings.Join(req.Header.Values(k), ",")
		}
		headers.WriteString(k + ":" + strings.TrimSpace(v) + "\n")
	}
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, headers.String(), strings.Join(names, ";"), payload}, "\n")
	scope := day + "/" + c.cfg.Region + "/s3/aws4_request"
	key := []byte("AWS4" + c.cfg.SecretKey)
	for _, part := range []string{day, c.cfg.Region, "s3", "aws4_request"} {
		key = mac(key, part)
	}
	signature := hex.EncodeToString(mac(key, "AWS4-HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+hash([]byte(canonical))))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.cfg.AccessKey+"/"+scope+", SignedHeaders="+strings.Join(names, ";")+", Signature="+signature)
}

func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
