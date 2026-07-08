/*
This file implements optional S3-compatible immutable deployment storage.
Metadata objects act as commit markers because object stores lack atomic rename.
Serving, sweeping, and health checks mirror the local filesystem semantics.
It depends on the AWS SDK for configuration, signing, and S3 operations,
plus standard context, JSON, MIME, path, streaming, and time packages.
*/
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
	"github.com/aws/smithy-go"
)

type s3Storage struct {
	// client owns signing, endpoint resolution, retries, and S3 wire behavior.
	client *s3.Client
	// bucket is required and is the sole switch from local to object storage.
	bucket string
	// prefix allows several Tabucom installations to share one bucket safely.
	prefix string
}

// newS3Storage uses the standard AWS credential chain. BaseEndpoint and
// path-style addressing are the only compatibility knobs required by R2,
// RustFS, and other S3-compatible services.
func newS3Storage(cfg Config) (*s3Storage, error) {
	// Loading configuration here preserves AWS support for environment variables,
	// shared credential files, container roles, and instance roles without adding
	// a second credential configuration surface to Tabucom.
	loaded, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(cfg.S3Region))
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	client := s3.NewFromConfig(loaded, func(options *s3.Options) {
		// An empty endpoint leaves AWS partition and regional endpoint selection to
		// the SDK. A supplied endpoint replaces only that resolution step.
		if cfg.S3Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.S3Endpoint)
		}
		options.UsePathStyle = cfg.S3PathStyle
	})
	return &s3Storage{client: client, bucket: cfg.S3Bucket, prefix: cfg.S3Prefix}, nil
}

// key constructs slash-separated object keys independently of the host OS.
// path.Join also prevents accidental duplicate separators in operator prefixes.
func (storage *s3Storage) key(parts ...string) string {
	all := append([]string{}, parts...)
	if storage.prefix != "" {
		all = append([]string{storage.prefix}, all...)
	}
	return path.Join(all...)
}

// siteKey keeps every immutable deployment below sites/<id>/.
func (storage *s3Storage) siteKey(id, name string) string {
	return storage.key("sites", id, name)
}

// commit uploads a fully validated local staging directory. S3 has no atomic
// directory rename, so .site.json is deliberately uploaded last and acts as the
// commit marker: readers never serve a prefix without valid metadata.
func (storage *s3Storage) commit(stage, id string) error {
	// Collect regular files first so upload order is explicit and deterministic.
	// ZIP validation has already rejected links and special files before here.
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
	// Sort ordinary files by name but force the root metadata object to the end.
	// A nested file also named .site.json is content, not the visibility marker.
	sort.Slice(files, func(i, j int) bool {
		iMetadata := files[i] == filepath.Join(stage, metadataName)
		jMetadata := files[j] == filepath.Join(stage, metadataName)
		if iMetadata != jMetadata {
			return !iMetadata
		}
		return files[i] < files[j]
	})

	// Each file is uploaded with a known length because R2 and several compatible
	// endpoints reject or mishandle request bodies whose length is unknown.
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
			// Best-effort cleanup keeps a failed publication from consuming storage.
			// Visibility still fails closed even if cleanup itself is unavailable.
			_ = file.Close()
			_ = storage.deleteSite(id)
			return statErr
		}
		contentType := mime.TypeByExtension(filepath.Ext(name))
		if contentType == "" {
			// nosniff is set while serving, so unknown types must remain downloads.
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
			// The metadata marker may have been attempted only on the final object.
			// Removing the whole random prefix is safe because IDs are immutable.
			_ = storage.deleteSite(id)
			return err
		}
	}
	return nil
}

// metadata reads the private commit marker used for expiry and SPA behavior.
// Missing, unreadable, or malformed metadata makes a deployment invisible.
func (storage *s3Storage) metadata(id string) (Metadata, error) {
	var metadata Metadata
	// The manifest is intentionally a normal object instead of S3 user metadata:
	// it is portable across providers and large enough for future compatible fields.
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

func (storage *s3Storage) siteExists(id string) (bool, error) {
	_, err := storage.client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(storage.bucket), Key: aws.String(storage.siteKey(id, metadataName)),
	})
	if err == nil {
		return true, nil
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return false, nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "404") {
		return false, nil
	}
	return false, err
}

// object opens one stored response body. The caller must close Body.
func (storage *s3Storage) object(id, name string) (*s3.GetObjectOutput, error) {
	return storage.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(storage.bucket), Key: aws.String(storage.siteKey(id, name)),
	})
}

// serveS3Site mirrors local serving semantics while streaming from object
// storage: exact expiry, private metadata, directory indexes, and SPA fallback.
func (s *Server) serveS3Site(w http.ResponseWriter, r *http.Request, id, requested string) {
	// Metadata is fetched first because its presence is the publication boundary.
	// Expiry is checked per request; a delayed sweep cannot extend availability.
	metadata, err := s.s3.metadata(id)
	now := s.cfg.Now().UTC()
	if err != nil || !metadata.ExpiresAt.After(now) {
		http.NotFound(w, r)
		return
	}
	if metadata.Password == nil && r.Method == http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeSite(w, r, id, metadata) {
		return
	}

	requested = normalizeSitePath(requested, r.URL.Path)
	// Metadata remains private even when traversal-like URL paths normalize to it.
	if requested == metadataName || strings.HasPrefix(requested, "../") {
		http.NotFound(w, r)
		return
	}
	objectName := requested
	object, err := s.s3.object(id, objectName)
	// Object stores have no directories, so emulate local directory indexes by
	// trying <path>/index.html after an extensionless exact lookup misses.
	if err != nil && path.Ext(requested) == "" {
		objectName = path.Join(requested, "index.html")
		object, err = s.s3.object(id, objectName)
	}
	// SPA fallback is limited to extensionless GET navigation. Assets and HEAD
	// requests retain ordinary not-found behavior, matching local storage.
	if err != nil && metadata.SPA && r.Method == http.MethodGet && path.Ext(requested) == "" {
		objectName = "index.html"
		object, err = s.s3.object(id, objectName)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer object.Body.Close()

	// Keep the same browser security and bounded cache policy as local files.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	if metadata.Password != nil {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", deploymentCacheControl(metadata.ExpiresAt, now))
	}
	if object.ContentType != nil {
		w.Header().Set("Content-Type", *object.ContentType)
	}
	size := int64(-1)
	if object.ContentLength != nil {
		size = *object.ContentLength
		w.Header().Set("Content-Length", strconv.FormatInt(*object.ContentLength, 10))
	}
	if shouldDecorateHTML(r, objectName, size) {
		body, err := io.ReadAll(object.Body)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		body = decorateHTML(body)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Del("Accept-Ranges")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	w.WriteHeader(http.StatusOK)
	// HEAD performs the same authorization and resolution but emits no body.
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, object.Body)
	}
}

// deleteSite removes every object belonging to one immutable deployment.
func (storage *s3Storage) deleteSite(id string) error {
	return storage.deletePrefix(storage.key("sites", id) + "/")
}

// deletePrefix paginates object listing and deletes each page in one S3 batch.
// S3-compatible APIs cap batch deletion at 1,000 objects, matching list pages.
func (storage *s3Storage) deletePrefix(prefix string) error {
	// Pagination is mandatory: one deployment may exceed a provider's single-page
	// listing limit even though Tabucom bounds the total number of uploaded files.
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
		// Copy keys into DeleteObjects' identifier shape without buffering the
		// complete deployment, which may contain the configured 10,000-file limit.
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

// sweep enforces retention and removes abandoned uploads. It first inventories
// object keys because S3 cannot list logical deployment directories directly.
func (storage *s3Storage) sweep(now time.Time) error {
	// newest distinguishes a live upload from a prefix abandoned by a crashed
	// publisher; metadata records whether the atomic commit marker exists.
	type foundSite struct {
		newest   time.Time
		metadata bool
	}
	found := make(map[string]foundSite)
	prefix := storage.key("sites") + "/"
	// List every object below the configured installation prefix. Invalid IDs are
	// ignored so cleanup cannot delete keys not allocated by Tabucom.
	paginator := s3.NewListObjectsV2Paginator(storage.client, &s3.ListObjectsV2Input{Bucket: aws.String(storage.bucket), Prefix: aws.String(prefix)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(context.Background())
		if err != nil {
			return err
		}
		for _, object := range page.Contents {
			// Split only once because the remainder is an arbitrary nested asset key.
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
		// ponytail: one-hour grace protects active uploads; use per-upload leases if
		// publication can legitimately take longer than an hour.
		if !site.metadata && now.Sub(site.newest) <= time.Hour {
			continue
		}
		metadata, err := storage.metadata(id)
		// Invalid metadata fails closed because retention cannot be established.
		// errors.Join lets cleanup continue and reports every failed deletion.
		if err != nil || !metadata.ExpiresAt.After(now) {
			joined = errors.Join(joined, storage.deleteSite(id))
		}
	}
	return joined
}

// health verifies real write, read, and delete permissions rather than merely
// checking configuration. The random key prevents concurrent probes colliding.
func (storage *s3Storage) health() error {
	id, err := randomID()
	if err != nil {
		return err
	}
	key := storage.key(".health", id)
	// Health objects live outside sites/ so the expiry inventory never interprets
	// an interrupted probe as an incomplete deployment.
	_, err = storage.client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(storage.bucket), Key: aws.String(key), Body: strings.NewReader("ok"), ContentLength: aws.Int64(2),
	})
	if err != nil {
		return err
	}
	// Always attempt deletion after the read so health probes do not accumulate.
	object, getErr := storage.client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(storage.bucket), Key: aws.String(key)})
	if getErr == nil {
		getErr = object.Body.Close()
	}
	_, deleteErr := storage.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(storage.bucket), Key: aws.String(key)})
	return errors.Join(getErr, deleteErr)
}
