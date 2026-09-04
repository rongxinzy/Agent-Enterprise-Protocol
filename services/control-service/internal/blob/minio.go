package blob

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store interface {
	Put(ctx context.Context, key string, content []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
	Ready(ctx context.Context) error
}

type MinioStore struct {
	client *minio.Client
	bucket string
}

func NewMinioStore(ctx context.Context, endpoint, accessKey, secretKey, bucket string, secure bool) (*MinioStore, error) {
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: secure})
	if err != nil {
		return nil, err
	}
	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			existsAfterCreate, checkErr := client.BucketExists(ctx, bucket)
			if checkErr != nil || !existsAfterCreate {
				return nil, err
			}
		}
	}
	return &MinioStore{client: client, bucket: bucket}, nil
}

func (s *MinioStore) Ready(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("configured MinIO bucket does not exist")
	}
	return nil
}

func (s *MinioStore) Put(ctx context.Context, key string, content []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(content), int64(len(content)), minio.PutObjectOptions{ContentType: "application/zip"})
	return err
}

func (s *MinioStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, err
	}
	return object, nil
}

func (s *MinioStore) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
