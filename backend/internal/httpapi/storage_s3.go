package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const maxS3ObjectBytes = 100 * 1024 * 1024

func (s *Server) s3Client(ctx context.Context) (*s3.Client, error) {
	if !s.s3Configured() {
		return nil, fmt.Errorf("S3/MinIO is not configured")
	}
	region := s.cfg.S3Region
	if region == "" {
		region = "us-east-1"
	}
	endpoint := strings.TrimSpace(s.cfg.S3Endpoint)
	if endpoint != "" && !strings.Contains(endpoint, "://") {
		scheme := "https"
		if !s.cfg.S3UseSSL {
			scheme = "http"
		}
		endpoint = scheme + "://" + endpoint
	}
	loaded, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			s.cfg.S3AccessKey, s.cfg.S3SecretKey, "",
		)),
	)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(loaded, func(options *s3.Options) {
		options.UsePathStyle = s.cfg.S3PathStyle
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	}), nil
}

func (s *Server) checkS3(ctx context.Context) error {
	client, err := s.s3Client(ctx)
	if err != nil {
		return err
	}
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.cfg.S3Bucket)})
	return err
}

func (s *Server) s3PutObject(ctx context.Context, key, contentType string, data []byte) error {
	client, err := s.s3Client(ctx)
	if err != nil {
		return err
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.cfg.S3Bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentType: aws.String(contentType),
		ContentLength: aws.Int64(int64(len(data))),
	})
	return err
}

func (s *Server) s3DeleteObject(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	client, err := s.s3Client(ctx)
	if err != nil {
		return err
	}
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.S3Bucket), Key: aws.String(key),
	})
	return err
}

func (s *Server) s3GetObject(ctx context.Context, key string) ([]byte, error) {
	client, err := s.s3Client(ctx)
	if err != nil {
		return nil, err
	}
	object, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.S3Bucket), Key: aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer object.Body.Close()
	data, err := io.ReadAll(io.LimitReader(object.Body, maxS3ObjectBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxS3ObjectBytes {
		return nil, fmt.Errorf("S3 object exceeds the download limit")
	}
	return data, nil
}

func (s *Server) syncS3Provider(ctx context.Context, sess *sessionRecord) error {
	client, err := s.s3Client(ctx)
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("tenants/%s/users/%s/files/", sess.User.TenantID, sess.User.ID)
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.S3Bucket), Prefix: aws.String(prefix),
	})
	var total int64
	seenKeys := []string{}
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, object := range page.Contents {
			if object.Key == nil {
				continue
			}
			key := *object.Key
			name := path.Base(key)
			mime := mimeForExtension(name)
			size := int64(0)
			if object.Size != nil {
				size = *object.Size
			}
			total += size
			seenKeys = append(seenKeys, key)
			// Rows created before the connector stored object keys as provider
			// identities have external_id='' but the same storage_key. Adopt them
			// instead of creating a duplicate row.
			updated, updateErr := s.store.DB.Exec(ctx, `
				UPDATE files SET external_id=$2, name=$3, mime=$4, size_bytes=$5,
				       previewable=$6, updated_at=now()
				WHERE user_id=$1 AND provider_id='s3' AND storage_key=$2 AND external_id=''
			`, sess.User.ID, key, name, mime, size, filePreviewable(mime))
			if updateErr == nil && updated.RowsAffected() > 0 {
				continue
			}
			_, _ = s.store.DB.Exec(ctx, `
				INSERT INTO files (id, tenant_id, user_id, provider_id, external_id, name, mime, path,
				                  description, size_bytes, storage_key, previewable, metadata)
				VALUES ($1,$2,$3,'s3',$4,$5,$6,'S3 / MinIO','Indexed from the configured bucket',$7,$4,$8,'{}')
				ON CONFLICT (user_id, provider_id, external_id) WHERE external_id <> ''
				DO UPDATE SET name=EXCLUDED.name, mime=EXCLUDED.mime, size_bytes=EXCLUDED.size_bytes,
				              storage_key=EXCLUDED.storage_key, previewable=EXCLUDED.previewable, updated_at=now()
			`, "file_s3_"+hashShort(key), sess.User.TenantID, sess.User.ID, key, name, mime, size,
				filePreviewable(mime), key)
		}
	}
	// A successful full listing is authoritative: remove local rows whose
	// object no longer exists in the bucket. Never do this on a partial listing.
	// storage_key is the physical object key for every S3-backed row.
	if len(seenKeys) == 0 {
		_, _ = s.store.DB.Exec(ctx, `
			DELETE FROM files WHERE user_id=$1 AND provider_id='s3' AND storage_key <> ''
		`, sess.User.ID)
	} else {
		_, _ = s.store.DB.Exec(ctx, `
			DELETE FROM files WHERE user_id=$1 AND provider_id='s3' AND storage_key <> ''
			  AND NOT (storage_key = ANY($2))
		`, sess.User.ID, seenKeys)
	}
	_, _ = s.store.DB.Exec(ctx, `
		UPDATE storage_accounts SET used_bytes=$2, last_sync_at=now(), status='connected', updated_at=now()
		WHERE user_id=$1 AND provider_id='s3'
	`, sess.User.ID, total)
	return nil
}

func filePreviewable(mime string) bool {
	return mime == "application/pdf" || strings.HasPrefix(mime, "image/")
}
