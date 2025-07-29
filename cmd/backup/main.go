package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/kelseyhightower/envconfig"
)

type BackupConfig struct {
	PostgresHost     string `envconfig:"POSTGRES_HOST" required:"true"`
	PostgresUser     string `envconfig:"POSTGRES_USER" required:"true"`
	PostgresPassword string `envconfig:"POSTGRES_PASSWORD" required:"true"`
	PostgresDB       string `envconfig:"POSTGRES_DB" required:"true"`
	BackupBucket     string `envconfig:"BACKUP_S3_BUCKET" required:"true"`
	BackupEndpoint   string `envconfig:"BACKUP_S3_ENDPOINT" required:"true"`
	BackupAccessKey  string `envconfig:"BACKUP_S3_ACCESS_KEY" required:"true"`
	BackupSecretKey  string `envconfig:"BACKUP_S3_SECRET_KEY" required:"true"`
	BackupRegion     string `envconfig:"BACKUP_S3_REGION" required:"true"`
	KeepBackups      int    `envconfig:"KEEP_BACKUPS" default:"4"`
}

func main() {
	log.Println("Starte Backup-Prozess...")

	var cfg BackupConfig
	err := envconfig.Process("", &cfg)
	if err != nil {
		log.Fatalf("Fehler beim Laden der Konfiguration: %v", err)
	}

	// 1. Datenbank-Dump erstellen
	dumpData, err := createDump(cfg)
	if err != nil {
		log.Fatalf("Fehler beim Erstellen des DB-Dumps: %v", err)
	}

	// 2. S3-Client erstellen
	s3Client, err := createS3Client(cfg)
	if err != nil {
		log.Fatalf("Fehler beim Erstellen des S3-Clients: %v", err)
	}

	// 3. Backup nach S3 hochladen
	fileName := fmt.Sprintf("backup-%s.sql.gz", time.Now().UTC().Format("2006-01-02T15-04-05Z"))
	err = uploadToS3(s3Client, cfg, fileName, dumpData)
	if err != nil {
		log.Fatalf("Fehler beim Hochladen nach S3: %v", err)
	}
	log.Printf("Backup erfolgreich nach s3://%s/%s hochgeladen", cfg.BackupBucket, fileName)

	// 4. Alte Backups rotieren
	err = rotateBackups(s3Client, cfg)
	if err != nil {
		log.Fatalf("Fehler bei der Rotation alter Backups: %v", err)
	}

	log.Println("Backup-Prozess erfolgreich abgeschlossen.")
}

func createDump(cfg BackupConfig) ([]byte, error) {
	cmd := exec.Command("pg_dump",
		"-h", cfg.PostgresHost,
		"-U", cfg.PostgresUser,
		"-d", cfg.PostgresDB,
		"-w", // Passwort wird über PGPASSWORD bereitgestellt
	)
	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", cfg.PostgresPassword))

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	if _, err := io.Copy(gzipWriter, stdout); err != nil {
		return nil, err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func createS3Client(cfg BackupConfig) (*s3.Client, error) {
	resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			URL: cfg.BackupEndpoint,
		}, nil
	})

	awsCfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithEndpointResolverWithOptions(resolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.BackupAccessKey, cfg.BackupSecretKey, "")),
		config.WithRegion(cfg.BackupRegion),
	)
	if err != nil {
		return nil, err
	}

	return s3.NewFromConfig(awsCfg), nil
}

func uploadToS3(client *s3.Client, cfg BackupConfig, key string, data []byte) error {
	_, err := client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(cfg.BackupBucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	return err
}

func rotateBackups(client *s3.Client, cfg BackupConfig) error {
	output, err := client.ListObjectsV2(context.TODO(), &s3.ListObjectsV2Input{
		Bucket: aws.String(cfg.BackupBucket),
	})
	if err != nil {
		return err
	}

	if len(output.Contents) <= cfg.KeepBackups {
		log.Printf("Weniger als %d Backups vorhanden, keine Rotation nötig.", cfg.KeepBackups)
		return nil
	}

	sort.Slice(output.Contents, func(i, j int) bool {
		return output.Contents[i].LastModified.After(*output.Contents[j].LastModified)
	})

	for _, obj := range output.Contents[cfg.KeepBackups:] {
		log.Printf("Lösche altes Backup: %s", *obj.Key)
		_, err := client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
			Bucket: aws.String(cfg.BackupBucket),
			Key:    obj.Key,
		})
		if err != nil {
			log.Printf("Fehler beim Löschen von %s: %v", *obj.Key, err)
		}
	}

	return nil
}
