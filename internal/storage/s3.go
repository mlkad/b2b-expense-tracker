package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// S3Store signs requests for any S3-compatible object store: AWS S3, MinIO,
// Cloudflare R2, Backblaze B2.
//
// The signing is written out here rather than taken from the AWS SDK. This
// service needs exactly one thing from S3 - a presigned URL - and the SDK is a
// large dependency surface to carry for a hundred lines of HMAC. Query-string
// SigV4 is also a fully specified algorithm with a stable definition, which is
// the kind of thing worth implementing directly; a REST client for the whole
// S3 API would not be.
//
// The trade is that a mistake here is silent until a request is rejected, so
// it is verified two ways: unit tests pin the canonical request and signature
// against fixed inputs, and the integration suite performs a real presigned
// upload and download against MinIO.
type S3Store struct {
	endpoint  *url.URL
	region    string
	bucket    string
	accessKey string
	secretKey string

	// pathStyle addresses the bucket as endpoint/bucket/key rather than
	// bucket.endpoint/key. MinIO and most self-hosted stores need it; AWS
	// deprecated it but still honours it for existing buckets.
	pathStyle bool

	client *http.Client
	now    func() time.Time
}

type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	PathStyle bool
}

func NewS3Store(cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("object storage needs an endpoint, bucket, access key and secret key")
	}
	if cfg.Region == "" {
		// us-east-1 is what S3-compatible stores accept when they do not care
		// about the region, and it is part of the signature - so it has to be
		// something rather than empty.
		cfg.Region = "us-east-1"
	}

	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("endpoint must be http or https, got %q", endpoint.Scheme)
	}

	return &S3Store{
		endpoint:  endpoint,
		region:    cfg.Region,
		bucket:    cfg.Bucket,
		accessKey: cfg.AccessKey,
		secretKey: cfg.SecretKey,
		pathStyle: cfg.PathStyle,
		client:    httpClient(),
		now:       time.Now,
	}, nil
}

// MaxPresignTTL is the ceiling SigV4 itself imposes on a presigned URL.
const MaxPresignTTL = 7 * 24 * time.Hour

func (s *S3Store) PresignPut(_ context.Context, key string, ttl time.Duration, c PutConstraints) (*PresignedRequest, error) {
	headers := map[string]string{}
	if c.ContentType != "" {
		headers["content-type"] = c.ContentType
	}
	if c.ChecksumSHA256 != "" {
		// Signing this header means the store computes the digest itself and
		// rejects the upload if it disagrees. Verification happens where the
		// bytes are, which is the only place it can be trusted.
		headers["x-amz-checksum-sha256"] = c.ChecksumSHA256
	}

	signed, expiresAt, err := s.presign(http.MethodPut, key, ttl, nil, headers)
	if err != nil {
		return nil, err
	}

	// Returned in the same casing the client must send. They are part of the
	// signature, so an altered one produces a 403 from the store rather than a
	// silently unconstrained upload.
	return &PresignedRequest{
		URL:       signed,
		Method:    http.MethodPut,
		Headers:   headers,
		ExpiresAt: expiresAt,
	}, nil
}

func (s *S3Store) PresignGet(_ context.Context, key string, ttl time.Duration, downloadName string) (string, error) {
	query := url.Values{}
	if downloadName != "" {
		// response-content-disposition makes the store serve the name the user
		// uploaded rather than the object key. It is signed, so a client
		// cannot rewrite it to something misleading.
		query.Set("response-content-disposition", contentDisposition(downloadName))
	}

	signed, _, err := s.presign(http.MethodGet, key, ttl, query, nil)
	return signed, err
}

func (s *S3Store) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	// A presigned HEAD rather than an Authorization header, so there is one
	// signing path in this file and no second one to get subtly wrong.
	signed, _, err := s.presign(http.MethodHead, key, time.Minute, nil, nil)
	if err != nil {
		return ObjectInfo{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, signed, nil)
	if err != nil {
		return ObjectInfo{}, err
	}
	// Not signed, and it does not need to be: unsigned headers are permitted
	// on a presigned request, and this one only asks the store to include a
	// digest it has already computed. Without it both S3 and MinIO omit the
	// checksum from HEAD entirely.
	req.Header.Set("x-amz-checksum-mode", "ENABLED")

	resp, err := s.client.Do(req)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ObjectInfo{}, ErrNotFound
	case resp.StatusCode == http.StatusForbidden:
		// S3 answers 403 rather than 404 for a missing object when the
		// credentials cannot list the bucket, so the two are indistinguishable
		// from here. Treating it as missing is the safe reading: the confirm
		// path refuses rather than recording an attachment it cannot see.
		return ObjectInfo{}, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return ObjectInfo{}, fmt.Errorf("%w: HEAD returned %d", ErrUnavailable, resp.StatusCode)
	}

	size, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	return ObjectInfo{
		Key:            key,
		SizeBytes:      size,
		ContentType:    resp.Header.Get("Content-Type"),
		ChecksumSHA256: resp.Header.Get("x-amz-checksum-sha256"),
	}, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	signed, _, err := s.presign(http.MethodDelete, key, time.Minute, nil, nil)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, signed, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	// S3 answers 204 for a successful delete and also for a key that was never
	// there, which makes deletion idempotent - the property the cleanup path
	// wants.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: DELETE returned %d", ErrUnavailable, resp.StatusCode)
	}
	return nil
}

// -----------------------------------------------------------------------------
// SigV4 query-string signing
// -----------------------------------------------------------------------------

const (
	algorithm       = "AWS4-HMAC-SHA256"
	service         = "s3"
	terminator      = "aws4_request"
	isoLayout       = "20060102T150405Z"
	dateLayout      = "20060102"
	unsignedPayload = "UNSIGNED-PAYLOAD"
)

// presign builds a signed URL for one request.
//
// The steps are the ones in the SigV4 specification, in order, and each is
// separated out because a mistake in any of them produces the same
// indistinguishable 403 from the store.
func (s *S3Store) presign(
	method, key string,
	ttl time.Duration,
	extraQuery url.Values,
	signedHeaders map[string]string,
) (string, time.Time, error) {
	if ttl <= 0 || ttl > MaxPresignTTL {
		return "", time.Time{}, fmt.Errorf("presign ttl must be between 1s and %s, got %s", MaxPresignTTL, ttl)
	}
	if key == "" {
		return "", time.Time{}, errors.New("presign requires an object key")
	}

	now := s.now().UTC()
	amzDate := now.Format(isoLayout)
	scopeDate := now.Format(dateLayout)
	scope := strings.Join([]string{scopeDate, s.region, service, terminator}, "/")

	host, canonicalPath := s.address(key)

	// Host is always signed. Without it the signature would not bind the URL
	// to a bucket, and a valid signature for one host would work against
	// another.
	headers := map[string]string{"host": host}
	for k, v := range signedHeaders {
		headers[strings.ToLower(k)] = v
	}

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		// Values are trimmed and internal runs of spaces collapsed, which the
		// specification requires and which is easy to miss - a header the
		// client sends with different spacing would then not verify.
		canonicalHeaders.WriteString(strings.Join(strings.Fields(headers[name]), " "))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaderList := strings.Join(names, ";")

	query := url.Values{}
	for k, vs := range extraQuery {
		for _, v := range vs {
			query.Add(k, v)
		}
	}
	query.Set("X-Amz-Algorithm", algorithm)
	query.Set("X-Amz-Credential", s.accessKey+"/"+scope)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", strconv.Itoa(int(ttl.Seconds())))
	query.Set("X-Amz-SignedHeaders", signedHeaderList)

	canonicalRequest := strings.Join([]string{
		method,
		canonicalPath,
		canonicalQuery(query),
		canonicalHeaders.String(),
		signedHeaderList,
		// The body is not signed: the client sends it directly and this
		// process never sees it. That is what UNSIGNED-PAYLOAD is for, and it
		// is why the checksum header exists - it is the only thing binding the
		// signature to the content.
		unsignedPayload,
	}, "\n")

	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hex.EncodeToString(hash([]byte(canonicalRequest))),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(s.signingKey(scopeDate), []byte(stringToSign)))
	query.Set("X-Amz-Signature", signature)

	// Assembled by concatenation rather than through url.URL.
	//
	// canonicalPath and canonicalQuery are already percent-encoded, because
	// that is the form the signature was computed over. Putting an encoded
	// path into url.URL.Path and calling String() encodes it a second time -
	// "%20" becomes "%2520" - and the store answers SignatureDoesNotMatch with
	// no indication of which of the dozen inputs was wrong. What is sent has to
	// be byte-for-byte what was signed.
	signed := s.endpoint.Scheme + "://" + host + canonicalPath + "?" + canonicalQuery(query)

	return signed, now.Add(ttl), nil
}

// address resolves the host and the canonical path for a key.
func (s *S3Store) address(key string) (host, canonicalPath string) {
	if s.pathStyle {
		return s.endpoint.Host, "/" + s.bucket + "/" + encodePath(key)
	}
	return s.bucket + "." + s.endpoint.Host, "/" + encodePath(key)
}

// signingKey derives the per-day, per-region, per-service key.
//
// The chain is what makes a leaked signing key useless outside the day and
// region it was derived for, which is the whole reason SigV4 is not a plain
// HMAC of the secret.
func (s *S3Store) signingKey(scopeDate string) []byte {
	k := hmacSHA256([]byte("AWS4"+s.secretKey), []byte(scopeDate))
	k = hmacSHA256(k, []byte(s.region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte(terminator))
}

// canonicalQuery renders the query string the way SigV4 requires: sorted by
// key, then by value, and RFC 3986 encoded.
//
// url.Values.Encode already sorts by key and encodes - but it encodes a space
// as "+", and SigV4 requires "%20". A single space in a filename inside
// response-content-disposition is enough to break every download.
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		values := append([]string(nil), q[k]...)
		sort.Strings(values)
		for _, v := range values {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(encodeSegment(k))
			b.WriteByte('=')
			b.WriteString(encodeSegment(v))
		}
	}
	return b.String()
}

// encodePath encodes an object key for the canonical URI, preserving "/" as a
// separator - S3 keys are not paths but they are addressed as if they were.
func encodePath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = encodeSegment(p)
	}
	return strings.Join(parts, "/")
}

// encodeSegment is RFC 3986 percent-encoding with the unreserved set that
// SigV4 specifies. url.QueryEscape is not usable: it encodes a space as "+"
// and leaves "+" itself alone, both of which produce a signature mismatch.
func encodeSegment(s string) string {
	const upperhex = "0123456789ABCDEF"

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&15])
		}
	}
	return b.String()
}

func contentDisposition(filename string) string {
	// Quotes and backslashes would let a filename close the parameter and add
	// another; newlines would split the header. The store echoes this value
	// straight into a response header.
	safe := strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == ';' || r == 0x7F {
			return '_'
		}
		return r
	}, filename)
	return `attachment; filename="` + safe + `"`
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hash(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// PresignBucketCreate signs a PUT against the bucket itself.
//
// It exists for test harnesses and for a first deployment into an empty
// account; the service never calls it at runtime, because creating a bucket is
// a provisioning step rather than something an expense API should be able to
// do. It is here rather than in the test so that bucket creation goes through
// the same signing code as everything else - a separate implementation for one
// call is a second thing that can be subtly wrong.
func (s *S3Store) PresignBucketCreate(ttl time.Duration) (string, error) {
	now := s.now().UTC()
	amzDate := now.Format(isoLayout)
	scopeDate := now.Format(dateLayout)
	scope := strings.Join([]string{scopeDate, s.region, service, terminator}, "/")

	host := s.endpoint.Host
	canonicalPath := "/" + encodeSegment(s.bucket)
	if !s.pathStyle {
		host = s.bucket + "." + s.endpoint.Host
		canonicalPath = "/"
	}

	query := url.Values{}
	query.Set("X-Amz-Algorithm", algorithm)
	query.Set("X-Amz-Credential", s.accessKey+"/"+scope)
	query.Set("X-Amz-Date", amzDate)
	query.Set("X-Amz-Expires", strconv.Itoa(int(ttl.Seconds())))
	query.Set("X-Amz-SignedHeaders", "host")

	canonicalRequest := strings.Join([]string{
		http.MethodPut, canonicalPath, canonicalQuery(query),
		"host:" + host + "\n", "host", unsignedPayload,
	}, "\n")

	stringToSign := strings.Join([]string{
		algorithm, amzDate, scope, hex.EncodeToString(hash([]byte(canonicalRequest))),
	}, "\n")

	query.Set("X-Amz-Signature", hex.EncodeToString(hmacSHA256(s.signingKey(scopeDate), []byte(stringToSign))))

	return s.endpoint.Scheme + "://" + host + canonicalPath + "?" + canonicalQuery(query), nil
}
