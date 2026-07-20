package docker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RealS3Client implements S3Uploader using AWS Signature Version 4.
// Tiny footprint, no external SDKs (vision.md §1).
type RealS3Client struct {
	client *http.Client
}

// NewS3Client wires a new S3 client.
func NewS3Client() *RealS3Client {
	return &RealS3Client{
		client: &http.Client{Timeout: 30 * time.Minute},
	}
}

// Upload uploads data to S3 using PUT.
func (s *RealS3Client) Upload(
	ctx context.Context,
	endpoint, bucket, region, key, accessKey, secretKey string,
	body io.Reader,
	size int64,
) error {
	return s.request(ctx, http.MethodPut, endpoint, bucket, region, key, accessKey, secretKey, body, size)
}

// Download downloads data from S3 using GET.
func (s *RealS3Client) Download(
	ctx context.Context,
	endpoint, bucket, region, key, accessKey, secretKey string,
) (io.ReadCloser, error) {
	u, err := s.buildURL(endpoint, bucket, key)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	s.sign(req, region, accessKey, secretKey, nil)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("s3 download status: %s", resp.Status)
	}

	return resp.Body, nil
}

func (s *RealS3Client) request(
	ctx context.Context,
	method, endpoint, bucket, region, key, accessKey, secretKey string,
	body io.Reader,
	size int64,
) error {
	u, err := s.buildURL(endpoint, bucket, key)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	req.ContentLength = size

	// Calculate payload hash for upload. For PUT, we can calculate SHA256 of payload.
	// Since we stream it, we can also use UNSIGNED-PAYLOAD or compute it if it's in memory.
	// To keep it simple and streamable, we use "UNSIGNED-PAYLOAD" for Signature V4 payload hash,
	// which is supported by all S3-compatible APIs.
	payloadHash := "UNSIGNED-PAYLOAD"

	s.sign(req, region, accessKey, secretKey, &payloadHash)

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("s3 response error: status %s, body: %s", resp.Status, string(respBody))
	}

	return nil
}

func (s *RealS3Client) buildURL(endpoint, bucket, key string) (*url.URL, error) {
	// Standardize endpoint scheme.
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}

	base, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	// Path-style routing (standard for self-hosted MinIO/S3-compatible targets).
	base.Path = fmt.Sprintf("/%s/%s", bucket, strings.TrimPrefix(key, "/"))
	return base, nil
}

func (s *RealS3Client) sign(req *http.Request, region, accessKey, secretKey string, payloadHash *string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	hash := "UNSIGNED-PAYLOAD"
	if payloadHash != nil {
		hash = *payloadHash
	}
	req.Header.Set("X-Amz-Content-Sha256", hash)
	req.Header.Set("Host", req.URL.Host)

	// Sign request.
	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, region)

	// Task 1: Canonical Request.
	canonicalURI := req.URL.EscapedPath()
	canonicalQuery := req.URL.Query().Encode()
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", req.URL.Host, hash, amzDate)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method, canonicalURI, canonicalQuery, canonicalHeaders, signedHeaders, hash,
	)

	// Task 2: String to Sign.
	h := sha256.New()
	h.Write([]byte(canonicalRequest))
	hashedCanonicalRequest := hex.EncodeToString(h.Sum(nil))

	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate, credentialScope, hashedCanonicalRequest,
	)

	// Task 3: Signature.
	signingKey := getSignatureKey(secretKey, dateStamp, region, "s3")
	signature := hmacSHA256(signingKey, []byte(stringToSign))

	// Task 4: Authorization Header.
	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, hex.EncodeToString(signature),
	)
	req.Header.Set("Authorization", authHeader)
}

func hmacSHA256(key []byte, data []byte) []byte {
	hash := hmac.New(sha256.New, key)
	hash.Write(data)
	return hash.Sum(nil)
}

func getSignatureKey(key string, dateStamp, regionName, serviceName string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+key), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(regionName))
	kService := hmacSHA256(kRegion, []byte(serviceName))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return kSigning
}
