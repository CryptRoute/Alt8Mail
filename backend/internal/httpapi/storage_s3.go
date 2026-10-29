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

func (s *Server) s3Client(ctx context.Context, tenantID string) (*s3.Client, error) {
	runtime, err := s.s3Runtime(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	region := runtime.Region
	if region == "" {
		region = "us-east-1"
	}
	endpoint := strings.TrimSpace(runtime.Endpoint)
	if endpoint != "" && !strings.Contains(endpoint, "://") {
		scheme := "https"
		if !runtime.UseSSL {
			scheme = "http"
		}
		endpoint = scheme + "://" + endpoint
	}
	loaded, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			runtime.AccessKey, runtime.SecretKey, "",
		)),
	)
	if err != nil {
		return nil, err
	}
	return s3.NewFromConfig(loaded, func(options *s3.Options) {
		options.UsePathStyle = runtime.PathStyle
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
	}), nil
}

func (s *Server) checkS3(ctx context.Context, tenantID string) error {
	client, err := s.s3Client(ctx, tenantID)
	if err != nil {
		return err
	}
	runtime, err := s.s3Runtime(ctx, tenantID)
	if err != nil {
		return err
	}
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(runtime.Bucket)})
	return err
}

func (s *Server) s3PutObject(ctx context.Context, tenantID, key, contentType string, data []byte) error {
	client, err := s.s3Client(ctx, tenantID)
	if err != nil {
		return err
	}
	runtime, err := s.s3Runtime(ctx, tenantID)
	if err != nil {
		return err
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(runtime.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentType: aws.String(contentType),
		ContentLength: aws.Int64(int64(len(data))),
	})
	return err
}

func (s *Server) s3DeleteObject(ctx context.Context, tenantID, key string) error {
	if key == "" {
		return nil
	}
	client, err := s.s3Client(ctx, tenantID)
	if err != nil {
		return err
	}
	runtime, err := s.s3Runtime(ctx, tenantID)
	if err != nil {
		return err
	}
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(runtime.Bucket), Key: aws.String(key),
	})
	return err
}

func (s *Server) s3GetObject(ctx context.Context, tenantID, key string) ([]byte, error) {
	client, err := s.s3Client(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	runtime, err := s.s3Runtime(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	object, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(runtime.Bucket), Key: aws.String(key),
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
	client, err := s.s3Client(ctx, sess.User.TenantID)
	if err != nil {
		return err
	}
	runtime, err := s.s3Runtime(ctx, sess.User.TenantID)
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("tenants/%s/users/%s/files/", sess.User.TenantID, sess.User.ID)
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{
		Bucket: aws.String(runtime.Bucket), Prefix: aws.String(prefix),
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
