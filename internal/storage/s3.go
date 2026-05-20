package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3 struct {
	client *s3.Client
	bucket string
}

func NewS3(endpoint, bucket, accessKey, secretKey, region string) (*S3, error) {
	if region == "" {
		region = "auto"
	}
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load S3 config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = true
	})

	return &S3{client: client, bucket: bucket}, nil
}

// Upload streams data to S3. The caller is responsible for size-limiting
// the reader before passing it in — we don't buffer the body here.
func (s *S3) Upload(ctx context.Context, key string, data io.Reader) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        data,
		ContentType: aws.String("application/gzip"),
	})
	if err != nil {
		return fmt.Errorf("upload to S3: %w", err)
	}
	return nil
}

// UploadBytes is a convenience wrapper for in-memory payloads. The byte
// slice must not be mutated until the call returns.
func (s *S3) UploadBytes(ctx context.Context, key string, data []byte) error {
	return s.Upload(ctx, key, bytes.NewReader(data))
}

func (s *S3) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("download from S3: %w", err)
	}
	return result.Body, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

// StreamObject downloads an S3 object and copies it to w. Returns the
// number of bytes written. Used by `relic-api backup --include-blobs`
// to fold every object into the tarball without staging the bucket on
// local disk first.
func (s *S3) StreamObject(ctx context.Context, key string, w io.Writer) (int64, error) {
	rc, err := s.Download(ctx, key)
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	return io.Copy(w, rc)
}

// PresignGet returns a pre-signed GetObject URL valid for ttl. Used
// by the trace-download handler (WS-2E) to redirect the client to S3
// directly instead of streaming through the API process.
func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	pre := s3.NewPresignClient(s.client)
	req, err := pre.PresignGetObject(ctx,
		&s3.GetObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		},
		s3.WithPresignExpires(ttl),
	)
	if err != nil {
		return "", fmt.Errorf("presign get: %w", err)
	}
	return req.URL, nil
}

// Ping verifies the bucket exists and credentials work. Used by
// /readyz so the deploy fails fast on misconfiguration rather than
// emitting NoSuchBucket on the first user upload. HeadBucket also
// returns 403 for a wrong-credential scenario, which is the other
// boot-time failure we want to catch.
func (s *S3) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	return err
}

// ListKeys returns every object key in the bucket. Used by the backup
// command to record what blobs the bundle references. Pagination
// continues until ContinuationToken is empty (S3 caps each page at
// 1000). On a large bucket this can be slow; backup is an
// administrator-triggered operation so latency is acceptable.
func (s *S3) ListKeys(ctx context.Context) ([]string, error) {
	var out []string
	var token *string
	for {
		resp, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list S3 keys: %w", err)
		}
		for _, obj := range resp.Contents {
			if obj.Key != nil {
				out = append(out, *obj.Key)
			}
		}
		if resp.IsTruncated == nil || !*resp.IsTruncated {
			break
		}
		token = resp.NextContinuationToken
	}
	return out, nil
}
