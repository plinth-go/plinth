// Package s3 adapts Amazon S3 or an S3-compatible service to the
// objectstore.Store contract. The AWS SDK is an optional dependency of this
// subpackage; applications that use localfs do not link it.
package s3

import (
	"context"
	"errors"
	"io"
	"mime"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/plinth-go/plinth/platform/objectstore"
)

// API is the narrow S3 client surface used by Store. The concrete
// *s3.Client from aws-sdk-go-v2 satisfies it.
type API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// Config selects one bucket and optional key namespace.
type Config struct {
	Bucket string
	// Prefix is prepended to every object key. A trailing slash is normalized.
	Prefix string
}

// Store implements objectstore.Store with S3 API calls.
type Store struct {
	client API
	bucket string
	prefix string
}

// New validates cfg and constructs a store. Prefix is relative to the bucket.
func New(client API, cfg Config) (*Store, error) {
	if isNilClient(client) {
		return nil, errors.New("s3 client is nil")
	}
	bucket := strings.TrimSpace(cfg.Bucket)
	if bucket == "" {
		return nil, errors.New("s3 bucket is required")
	}
	if strings.ContainsAny(bucket, "\r\n\x00") {
		return nil, errors.New("s3 bucket must not contain control characters")
	}
	prefix := strings.TrimSpace(cfg.Prefix)
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/") + "/"
		if _, err := objectstore.NormalizePrefix(prefix); err != nil {
			return nil, err
		}
	}
	return &Store{client: client, bucket: bucket, prefix: prefix}, nil
}

func (s *Store) Put(ctx context.Context, key string, source io.Reader, size int64, opts objectstore.PutOptions) (objectstore.Object, error) {
	key, err := s.key(key)
	if err != nil {
		return objectstore.Object{}, err
	}
	if source == nil {
		return objectstore.Object{}, errors.New("s3 object body is nil")
	}
	if size < 0 {
		return objectstore.Object{}, errors.New("s3 object size must not be negative")
	}
	contentType := strings.TrimSpace(opts.ContentType)
	if contentType == "" {
		contentType = contentTypeForKey(key)
	}
	input := &s3.PutObjectInput{
		Bucket:        &s.bucket,
		Key:           &key,
		Body:          source,
		ContentLength: &size,
		ContentType:   &contentType,
	}
	if opts.CacheControl != "" {
		input.CacheControl = &opts.CacheControl
	}
	if len(opts.Metadata) != 0 {
		input.Metadata = cloneMetadata(opts.Metadata)
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		return objectstore.Object{}, err
	}
	return s.statKey(ctx, key)
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, objectstore.Object, error) {
	key, err := s.key(key)
	if err != nil {
		return nil, objectstore.Object{}, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		if isNotFound(err) {
			return nil, objectstore.Object{}, objectstore.ErrNotFound
		}
		return nil, objectstore.Object{}, err
	}
	if output == nil || output.Body == nil {
		return nil, objectstore.Object{}, errors.New("s3 returned an empty object body")
	}
	return output.Body, objectFromS3(s.relativeKey(key), output.ContentLength, output.ContentType, output.LastModified), nil
}

func (s *Store) Stat(ctx context.Context, key string) (objectstore.Object, error) {
	key, err := s.key(key)
	if err != nil {
		return objectstore.Object{}, err
	}
	return s.statKey(ctx, key)
}

func (s *Store) statKey(ctx context.Context, key string) (objectstore.Object, error) {
	output, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		if isNotFound(err) {
			return objectstore.Object{}, objectstore.ErrNotFound
		}
		return objectstore.Object{}, err
	}
	if output == nil {
		return objectstore.Object{}, errors.New("s3 returned an empty head response")
	}
	return objectFromS3(s.relativeKey(key), output.ContentLength, output.ContentType, output.LastModified), nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	key, err := s.key(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	return err
}

func (s *Store) List(ctx context.Context, prefix string, limit int) ([]objectstore.Object, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("object list limit must be between 1 and 1000")
	}
	prefix, err := objectstore.NormalizePrefix(prefix)
	if err != nil {
		return nil, err
	}
	fullPrefix := s.prefix + prefix
	objects := make([]objectstore.Object, 0, limit)
	var continuation *string
	for len(objects) < limit {
		remaining := int32(limit - len(objects))
		output, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &s.bucket, Prefix: &fullPrefix, MaxKeys: &remaining,
			ContinuationToken: continuation,
		})
		if err != nil {
			return nil, err
		}
		if output == nil {
			break
		}
		for _, item := range output.Contents {
			if item.Key == nil {
				continue
			}
			key := strings.TrimPrefix(*item.Key, s.prefix)
			if key == "" || strings.HasSuffix(key, "/") {
				continue
			}
			if _, err := objectstore.NormalizeKey(key); err != nil {
				continue
			}
			objects = append(objects, objectFromS3(key, item.Size, nil, item.LastModified))
			if len(objects) == limit {
				break
			}
		}
		if output.IsTruncated == nil || !*output.IsTruncated || output.NextContinuationToken == nil || *output.NextContinuationToken == "" {
			break
		}
		continuation = output.NextContinuationToken
	}
	return objects, nil
}

func (s *Store) key(key string) (string, error) {
	key, err := objectstore.NormalizeKey(key)
	if err != nil {
		return "", err
	}
	return s.prefix + key, nil
}

func (s *Store) relativeKey(key string) string {
	return strings.TrimPrefix(key, s.prefix)
}

func objectFromS3(key string, size *int64, contentType *string, modified *time.Time) objectstore.Object {
	object := objectstore.Object{Key: key, ContentType: contentTypeForKey(key)}
	if size != nil {
		object.Size = *size
	}
	if contentType != nil && strings.TrimSpace(*contentType) != "" {
		object.ContentType = *contentType
	}
	if modified != nil {
		object.ModifiedAt = modified.UTC().Truncate(time.Second)
	}
	return object
}

func contentTypeForKey(key string) string {
	if contentType := mime.TypeByExtension(filepath.Ext(key)); contentType != "" {
		return contentType
	}
	return "application/octet-stream"
}

func cloneMetadata(metadata map[string]string) map[string]string {
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func isNotFound(err error) bool {
	if errors.Is(err, objectstore.ErrNotFound) {
		return true
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NoSuchKey", "NotFound", "NoSuchBucket":
		return true
	default:
		return false
	}
}

func isNilClient(client API) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ objectstore.Store = (*Store)(nil)
var _ API = (*s3.Client)(nil)
