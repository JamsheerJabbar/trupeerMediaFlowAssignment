package storage

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var (
	client *minio.Client
	bucket string
)

func Init() error {
	endpoint := os.Getenv("MINIO_ENDPOINT")
	accessKey := os.Getenv("MINIO_ACCESS_KEY")
	secretKey := os.Getenv("MINIO_SECRET_KEY")
	bucket = os.Getenv("MINIO_BUCKET")

	var err error
	client, err = minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: false,
	})
	if err != nil {
		return fmt.Errorf("init minio: %w", err)
	}
	return nil
}

func UploadFile(ctx context.Context, data []byte, objectPath, contentType string) (string, error) {
	reader := bytes.NewReader(data)
	_, err := client.PutObject(ctx, bucket, objectPath, reader, int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("upload %s: %w", objectPath, err)
	}
	return objectPath, nil
}

func DownloadFile(ctx context.Context, objectPath string) ([]byte, error) {
	obj, err := client.GetObject(ctx, bucket, objectPath, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", objectPath, err)
	}
	defer obj.Close()

	buf := new(bytes.Buffer)
	if _, err = buf.ReadFrom(obj); err != nil {
		return nil, fmt.Errorf("read %s: %w", objectPath, err)
	}
	return buf.Bytes(), nil
}

func FileExists(ctx context.Context, objectPath string) bool {
	_, err := client.StatObject(ctx, bucket, objectPath, minio.StatObjectOptions{})
	return err == nil
}
