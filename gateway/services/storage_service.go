package services

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var (
	minioClient *minio.Client
	minioBucket string
)

func InitStorage() error {
	endpoint := os.Getenv("MINIO_ENDPOINT")
	accessKey := os.Getenv("MINIO_ACCESS_KEY")
	secretKey := os.Getenv("MINIO_SECRET_KEY")
	minioBucket = os.Getenv("MINIO_BUCKET")

	var err error
	minioClient, err = minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: false,
	})
	if err != nil {
		return fmt.Errorf("init minio client: %w", err)
	}
	return nil
}

func UploadFile(ctx context.Context, data []byte, objectPath, contentType string) (string, error) {
	reader := bytes.NewReader(data)
	_, err := minioClient.PutObject(ctx, minioBucket, objectPath, reader, int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("upload %s: %w", objectPath, err)
	}
	return objectPath, nil
}

func DownloadFile(ctx context.Context, objectPath string) ([]byte, error) {
	obj, err := minioClient.GetObject(ctx, minioBucket, objectPath, minio.GetObjectOptions{})
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
	_, err := minioClient.StatObject(ctx, minioBucket, objectPath, minio.StatObjectOptions{})
	return err == nil
}

func GetPresignedURL(ctx context.Context, objectPath string, expiry time.Duration) (string, error) {
	reqParams := make(url.Values)
	u, err := minioClient.PresignedGetObject(ctx, minioBucket, objectPath, expiry, reqParams)
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", objectPath, err)
	}
	return u.String(), nil
}

func InputPathBuilder(jobID, filename string) string {
	return fmt.Sprintf("jobs/%s/input/%s", jobID, filename)
}

func OutputPathBuilder(jobID, stageID, ext string) string {
	return fmt.Sprintf("jobs/%s/stages/%s/output.%s", jobID, stageID, ext)
}
