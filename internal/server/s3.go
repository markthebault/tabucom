package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type s3Storage struct {
	client *s3.Client
	bucket string
	prefix string
}

func newS3Storage(cfg Config) (*s3Storage, error) {
	loaded, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(cfg.S3Region))
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	client := s3.NewFromConfig(loaded, func(options *s3.Options) {
		if cfg.S3Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.S3Endpoint)
		}
		options.UsePathStyle = cfg.S3PathStyle
	})
	return &s3Storage{client: client, bucket: cfg.S3Bucket, prefix: cfg.S3Prefix}, nil
}

func (storage *s3Storage) key(parts ...string) string {
	all := append([]string{}, parts...)
	if storage.prefix != "" {
		all = append([]string{storage.prefix}, all...)
	}
	return path.Join(all...)
}

func (storage *s3Storage) siteKey(id, name string) string {
	return storage.key("sites", id, name)
}

func (storage *s3Storage) commit(stage, id string) error {
	var files []string
	err := filepath.WalkDir(stage, func(name string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		files = append(files, name)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool {
		iMetadata := files[i] == filepath.Join(stage, metadataName)
		jMetadata := files[j] == filepath.Join(stage, metadataName)
		if iMetadata != jMetadata {
			return !iMetadata
		}
		return files[i] < files[j]
	})

	for _, name := range files {
		relative, err := filepath.Rel(stage, name)
		if err != nil {
			return err
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			_ = storage.deleteSite(id)
			return statErr
		}
		contentType := mime.TypeByExtension(filepath.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		_, putErr := storage.client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket:        aws.String(storage.bucket),
			Key:           aws.String(storage.siteKey(id, filepath.ToSlash(relative))),
			Body:          file,
			ContentLength: aws.Int64(info.Size()),
			ContentType:   aws.String(contentType),
		})
		closeErr := file.Close()
		if err := errors.Join(putErr, closeErr); err != nil {
			_ = storage.deleteSite(id)
			return err
		}
	}
	return nil
}

func (storage *s3Storage) metadata(id string) (Metadata, error) {
	var metadata Metadata
	object, err := storage.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(storage.bucket), Key: aws.String(storage.siteKey(id, metadataName)),
	})
	if err != nil {
		return metadata, err
	}
	defer object.Body.Close()
	err = json.NewDecoder(object.Body).Decode(&metadata)
	return metadata, err
}

func (storage *s3Storage) object(id, name string) (*s3.GetObjectOutput, error) {
	return storage.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(storage.bucket), Key: aws.String(storage.siteKey(id, name)),
	})
}

func (s *Server) serveS3Site(w http.ResponseWriter, r *http.Request, id, requested string) {
	metadata, err := s.s3.metadata(id)
	now := s.cfg.Now().UTC()
	if err != nil || !metadata.ExpiresAt.After(now) {
		http.NotFound(w, r)
		return
	}

	requested = normalizeSitePath(requested, r.URL.Path)
	if requested == metadataName || strings.HasPrefix(requested, "../") {
		http.NotFound(w, r)
		return
	}
	object, err := s.s3.object(id, requested)
	if err != nil && path.Ext(requested) == "" {
		object, err = s.s3.object(id, path.Join(requested, "index.html"))
	}
	if err != nil && metadata.SPA && r.Method == http.MethodGet && path.Ext(requested) == "" {
		object, err = s.s3.object(id, "index.html")
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer object.Body.Close()

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	w.Header().Set("Cache-Control", deploymentCacheControl(metadata.ExpiresAt, now))
	if object.ContentType != nil {
		w.Header().Set("Content-Type", *object.ContentType)
	}
	if object.ContentLength != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*object.ContentLength, 10))
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, object.Body)
	}
}

func (storage *s3Storage) deleteSite(id string) error {
	return storage.deletePrefix(storage.key("sites", id) + "/")
}

func (storage *s3Storage) deletePrefix(prefix string) error {
	paginator := s3.NewListObjectsV2Paginator(storage.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(storage.bucket), Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(context.Background())
		if err != nil {
			return err
		}
		if len(page.Contents) == 0 {
			continue
		}
		objects := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, object := range page.Contents {
			objects = append(objects, types.ObjectIdentifier{Key: object.Key})
		}
		if _, err := storage.client.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
			Bucket: aws.String(storage.bucket), Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (storage *s3Storage) sweep(now time.Time) error {
	type foundSite struct {
		newest   time.Time
		metadata bool
	}
	found := make(map[string]foundSite)
	prefix := storage.key("sites") + "/"
	paginator := s3.NewListObjectsV2Paginator(storage.client, &s3.ListObjectsV2Input{Bucket: aws.String(storage.bucket), Prefix: aws.String(prefix)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(context.Background())
		if err != nil {
			return err
		}
		for _, object := range page.Contents {
			remainder := strings.TrimPrefix(aws.ToString(object.Key), prefix)
			parts := strings.SplitN(remainder, "/", 2)
			if len(parts) != 2 || !validID(parts[0]) {
				continue
			}
			site := found[parts[0]]
			if object.LastModified != nil && object.LastModified.After(site.newest) {
				site.newest = *object.LastModified
			}
			site.metadata = site.metadata || parts[1] == metadataName
			found[parts[0]] = site
		}
	}

	var joined error
	for id, site := range found {
		if !site.metadata && now.Sub(site.newest) <= time.Hour {
			continue
		}
		metadata, err := storage.metadata(id)
		if err != nil || !metadata.ExpiresAt.After(now) {
			joined = errors.Join(joined, storage.deleteSite(id))
		}
	}
	return joined
}

func (storage *s3Storage) health() error {
	id, err := randomID()
	if err != nil {
		return err
	}
	key := storage.key(".health", id)
	_, err = storage.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(storage.bucket), Key: aws.String(key), Body: strings.NewReader("ok"), ContentLength: aws.Int64(2),
	})
	if err != nil {
		return err
	}
	object, getErr := storage.client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(storage.bucket), Key: aws.String(key)})
	if getErr == nil {
		getErr = object.Body.Close()
	}
	_, deleteErr := storage.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(storage.bucket), Key: aws.String(key)})
	return errors.Join(getErr, deleteErr)
}
