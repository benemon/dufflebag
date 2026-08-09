// Package objectstore stores immutable SBOM blobs in an S3-compatible service.
package objectstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsretry "github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const maxAttempts = 3

// Config identifies one S3-compatible bucket.
type Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

// Store reads and writes one S3-compatible bucket.
//
// It remembers whether the bucket answered the last time anything asked, so
// the health probe can report the store's state without reaching for it.
// Health is polled far harder than the store is used, and an unauthenticated
// endpoint that opens a connection per request is a lever on infrastructure.
type Store struct {
	client *s3.Client
	bucket string
	// reachable holds the outcome of the most recent operation. The startup
	// check writes it before any request arrives, so it is never unset.
	reachable atomic.Bool
}

// Reachable reports what the last operation observed, not what is true now.
// Nothing has touched the bucket since, so nothing newer is known.
func (s *Store) Reachable() bool { return s.reachable.Load() }

// observe records what an operation saw. Every call that reaches the bucket
// passes through here, so the answer stays as fresh as the traffic.
func (s *Store) observe(err error) {
	s.reachable.Store(err == nil)
}

func New(config Config) (*Store, error) {
	configured := []struct{ name, value string }{
		{"endpoint", config.Endpoint},
		{"region", config.Region},
		{"bucket", config.Bucket},
		{"access_key", config.AccessKey},
		{"secret_key", config.SecretKey},
	}
	missing := make([]string, 0, len(configured))
	for _, setting := range configured {
		if setting.value == "" {
			missing = append(missing, setting.name)
		}
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("%s must be set", strings.Join(missing, ", "))
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return nil, fmt.Errorf("endpoint must be an absolute HTTP(S) URL")
	}
	client := s3.NewFromConfig(aws.Config{
		Region:       config.Region,
		BaseEndpoint: aws.String(config.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, ""),
		HTTPClient:   http.DefaultClient,
		Retryer: func() aws.Retryer {
			return awsretry.AddWithMaxAttempts(awsretry.NewStandard(), maxAttempts)
		},
	}, func(options *s3.Options) {
		// Ceph RGW interprets non-matching hostnames as virtual-host bucket
		// names. Path style also works with AWS and other S3-compatible stores.
		options.UsePathStyle = true
	})
	return &Store{client: client, bucket: config.Bucket}, nil
}

// CheckBucket verifies that the deployment-provisioned bucket is available.
func (s *Store) CheckBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket})
	s.observe(err)
	if err != nil {
		return fmt.Errorf("check object storage bucket %q: %w", s.bucket, err)
	}
	return nil
}

// Key returns an immutable, tenant-qualified key for one SBOM value. Prefixes
// organize tenants but do not enforce tenancy; Postgres RLS protects locators.
func Key(organizationID, projectID, buildID, name string, data []byte) string {
	identitySum := sha256.Sum256([]byte(buildID + "\x00" + name))
	dataSum := sha256.Sum256(data)
	return fmt.Sprintf(
		"t/%s/%s/s/%s/%s",
		strings.ReplaceAll(organizationID, "-", ""),
		strings.ReplaceAll(projectID, "-", ""),
		hex.EncodeToString(identitySum[:16]),
		hex.EncodeToString(dataSum[:]),
	)
}

func (s *Store) Put(ctx context.Context, key string, data []byte) error {
	return s.put(ctx, key, data, "")
}

// TranscriptClassTag marks scan-transcript objects for bucket lifecycle rules.
// Transcript keys are per-tenant (t/<org>/<proj>/x/...), so no literal prefix
// rule can match them all; a tag filter can, which is how orphans from the two
// documented crash windows are collected without a sweeper racing in-flight
// writes (duf-umu7). The tag names a class, never content.
const TranscriptClassTag = "dufflebag-class=transcript"

// PutTranscript stores a scan transcript carrying TranscriptClassTag.
func (s *Store) PutTranscript(ctx context.Context, key string, data []byte) error {
	return s.put(ctx, key, data, TranscriptClassTag)
}

func (s *Store) put(ctx context.Context, key string, data []byte, tagging string) error {
	input := &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	}
	if tagging != "" {
		input.Tagging = &tagging
	}
	_, err := s.client.PutObject(ctx, input)
	s.observe(err)
	if err != nil {
		return fmt.Errorf("put object %q: %w", key, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	object, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		s.observe(err)
		return nil, fmt.Errorf("get object %q: %w", key, err)
	}
	// The store can accept the request and fail while streaming, so the answer
	// is not known until the body is read. Observing the accepted request alone
	// would report a healthy store to the probe while failing this download.
	data, readErr := io.ReadAll(object.Body)
	closeErr := object.Body.Close()
	s.observe(errors.Join(readErr, closeErr))
	if readErr != nil {
		return nil, fmt.Errorf("read object %q: %w", key, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close object %q: %w", key, closeErr)
	}
	return data, nil
}

// TranscriptKey returns the immutable, tenant-qualified key for one scan-run
// transcript. The x segment keeps transcripts apart from SBOM values.
func TranscriptKey(organizationID, projectID, runID string, data []byte) string {
	identitySum := sha256.Sum256([]byte(runID))
	dataSum := sha256.Sum256(data)
	return fmt.Sprintf(
		"t/%s/%s/x/%s/%s",
		strings.ReplaceAll(organizationID, "-", ""),
		strings.ReplaceAll(projectID, "-", ""),
		hex.EncodeToString(identitySum[:16]),
		hex.EncodeToString(dataSum[:]),
	)
}

// Delete removes one object. Deleting an absent key succeeds — expiry reruns
// must be idempotent.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	s.observe(err)
	if err != nil {
		return fmt.Errorf("delete object %q: %w", key, err)
	}
	return nil
}
