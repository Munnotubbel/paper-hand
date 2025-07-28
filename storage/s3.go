package storage

import (
	"bytes"
	"context"
	"fmt"

	"paper-hand/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// NewS3Client erstellt einen S3-Client für Strato HiDrive.
func NewS3Client(cfg *config.Config) (*s3.Client, error) {
	resolver := aws.EndpointResolverWithOptionsFunc(
		func(service, region string, options ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:               cfg.StratoS3URL,
				SigningRegion:     cfg.StratoS3Region,
				HostnameImmutable: true,
			}, nil
		},
	)
	awsCfg, err := awsconfig.LoadDefaultConfig(context.TODO(),
		awsconfig.WithRegion(cfg.StratoS3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.StratoS3Key, cfg.StratoS3Secret, "")),
		awsconfig.WithEndpointResolverWithOptions(resolver),
	)
	if err != nil {
		return nil, err
	}

	return s3.NewFromConfig(awsCfg), nil
}

// UploadFile lädt eine Datei ins S3 hoch und gibt den Link zurück.
func UploadFile(client *s3.Client, bucket, key string, data []byte, cfg *config.Config) (string, error) {
	_, err := client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: &bucket,
		Key:    &key,
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return "", err
	}
	link := fmt.Sprintf("%s/%s/%s", cfg.StratoS3URL, bucket, key)
	return link, nil
}
