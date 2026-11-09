package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/plinth-go/plinth/platform/objectstore"
)

type fakeObject struct {
	body        string
	contentType string
	modified    time.Time
}

type fakeAPI struct {
	objects map[string]fakeObject
	put     *s3.PutObjectInput
}

func newFakeAPI() *fakeAPI { return &fakeAPI{objects: make(map[string]fakeObject)} }

func (f *fakeAPI) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.put = input
	body, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	// net/http rejects a request body whose length does not match
	// Request.ContentLength. Mirror that transport invariant here so the
	// adapter cannot accidentally truncate or pad the caller's stream.
	if input.ContentLength == nil || int64(len(body)) != *input.ContentLength {
		return nil, fmt.Errorf("put body length %d does not match content length %v", len(body), input.ContentLength)
	}
	contentType := ""
	if input.ContentType != nil {
		contentType = *input.ContentType
	}
	f.objects[*input.Key] = fakeObject{body: string(body), contentType: contentType, modified: time.Now().UTC()}
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeAPI) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	object, ok := f.objects[*input.Key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	}
	length := int64(len(object.body))
	return &s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(object.body)), ContentLength: &length,
		ContentType: &object.contentType, LastModified: &object.modified,
	}, nil
}

func (f *fakeAPI) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	object, ok := f.objects[*input.Key]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	}
	length := int64(len(object.body))
	return &s3.HeadObjectOutput{ContentLength: &length, ContentType: &object.contentType, LastModified: &object.modified}, nil
}

func (f *fakeAPI) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	delete(f.objects, *input.Key)
	return &s3.DeleteObjectOutput{}, nil
}

func (f *fakeAPI) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	output := &s3.ListObjectsV2Output{}
	limit := int32(1000)
	if input.MaxKeys != nil {
		limit = *input.MaxKeys
	}
	for key, object := range f.objects {
		if !strings.HasPrefix(key, *input.Prefix) {
			continue
		}
		length := int64(len(object.body))
		modified := object.modified
		output.Contents = append(output.Contents, types.Object{Key: &key, Size: &length, LastModified: &modified})
		if int32(len(output.Contents)) >= limit {
			break
		}
	}
	return output, nil
}

func TestStoreOperationsAndPrefix(t *testing.T) {
	api := newFakeAPI()
	store, err := New(api, Config{Bucket: "bucket", Prefix: "app"})
	if err != nil {
		t.Fatal(err)
	}
	object, err := store.Put(context.Background(), "images/logo.png", strings.NewReader("hello"), 5, objectstore.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if object.Key != "images/logo.png" || object.Size != 5 || object.ContentType != "image/png" {
		t.Fatalf("put object = %#v", object)
	}
	if api.put == nil || *api.put.Key != "app/images/logo.png" || *api.put.ContentLength != 5 {
		t.Fatalf("s3 put input = %#v", api.put)
	}

	reader, object, err := store.Open(context.Background(), "images/logo.png")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if string(body) != "hello" || object.Key != "images/logo.png" || object.ContentType != "image/png" {
		t.Fatalf("open = %q %#v", body, object)
	}

	listed, err := store.List(context.Background(), "images/", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Key != "images/logo.png" {
		t.Fatalf("list = %#v", listed)
	}

	if err := store.Delete(context.Background(), "images/logo.png"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(context.Background(), "images/logo.png"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("missing stat = %v", err)
	}
}

func TestStoreValidationAndErrors(t *testing.T) {
	if _, err := New(nil, Config{Bucket: "bucket"}); err == nil {
		t.Fatal("nil API was accepted")
	}
	api := newFakeAPI()
	if _, err := New(api, Config{}); err == nil {
		t.Fatal("empty bucket was accepted")
	}
	if _, err := New(api, Config{Bucket: "bucket", Prefix: "bad//prefix"}); err == nil {
		t.Fatal("invalid prefix was accepted")
	}
	store, err := New(api, Config{Bucket: "bucket"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "key", nil, 1, objectstore.PutOptions{}); err == nil {
		t.Fatal("nil body was accepted")
	}
	if _, err := store.Put(context.Background(), "key", strings.NewReader("x"), -1, objectstore.PutOptions{}); err == nil {
		t.Fatal("negative size was accepted")
	}
	if _, err := store.Put(context.Background(), "key", strings.NewReader("toolong"), 3, objectstore.PutOptions{}); err == nil {
		t.Fatal("source larger than declared size was accepted")
	}
	if _, err := store.Put(context.Background(), "key", strings.NewReader("x"), 3, objectstore.PutOptions{}); err == nil {
		t.Fatal("source smaller than declared size was accepted")
	}
	if _, err := store.List(context.Background(), "", 0); err == nil {
		t.Fatal("zero list limit was accepted")
	}
	if _, _, err := store.Open(context.Background(), "missing"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("missing open = %v", err)
	}
}
