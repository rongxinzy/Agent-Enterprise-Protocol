package blob

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func minioEndpoint(server *httptest.Server) string {
	return strings.TrimPrefix(server.URL, "http://")
}

func writeS3Error(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/xml")
	response.WriteHeader(status)
	_, _ = fmt.Fprintf(response, `<Error><Code>%s</Code><Message>test error</Message><Resource>/skills</Resource><RequestId>request-1</RequestId></Error>`, code)
}

func newTestMinioStore(t *testing.T, handler http.HandlerFunc) *MinioStore {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := minio.New(minioEndpoint(server), &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Secure: false,
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &MinioStore{client: client, bucket: "skills"}
}

func TestNewMinioStoreAndObjectLifecycle(t *testing.T) {
	archive := []byte("PK\x03\x04test-archive")
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodHead && strings.TrimSuffix(request.URL.Path, "/") == "/skills":
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Query().Has("location"):
			response.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(response, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
		case request.Method == http.MethodPut && request.URL.Path == "/skills/packages/test.zip":
			if request.Header.Get("Content-Type") != "application/zip" {
				t.Errorf("PutObject Content-Type = %q", request.Header.Get("Content-Type"))
			}
			uploaded, _ = io.ReadAll(request.Body)
			response.Header().Set("ETag", `"etag-1"`)
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodHead && request.URL.Path == "/skills/packages/test.zip":
			response.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			response.Header().Set("ETag", `"etag-1"`)
			response.Header().Set("Last-Modified", "Sat, 12 Sep 2026 00:00:00 GMT")
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/skills/packages/test.zip":
			response.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			response.Header().Set("ETag", `"etag-1"`)
			response.Header().Set("Last-Modified", "Sat, 12 Sep 2026 00:00:00 GMT")
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(archive)
		case request.Method == http.MethodDelete && request.URL.Path == "/skills/packages/test.zip":
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected MinIO request: %s %s", request.Method, request.URL.String())
			response.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	store, err := NewMinioStore(context.Background(), minioEndpoint(server), "access", "secret", "skills", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	if err := store.Put(context.Background(), "packages/test.zip", archive); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if !strings.Contains(string(uploaded), string(archive)) {
		t.Fatalf("uploaded content = %q", uploaded)
	}
	object, err := store.Get(context.Background(), "packages/test.zip")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	downloaded, err := io.ReadAll(object)
	if closeErr := object.Close(); err == nil {
		err = closeErr
	}
	if err != nil || string(downloaded) != string(archive) {
		t.Fatalf("downloaded content = %q, error = %v", downloaded, err)
	}
	if err := store.Delete(context.Background(), "packages/test.zip"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}

func TestNewMinioStoreCreatesMissingBucket(t *testing.T) {
	headCalls := 0
	putCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Query().Has("location") {
			response.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(response, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		switch request.Method {
		case http.MethodHead:
			headCalls++
			if putCalls == 0 {
				writeS3Error(response, http.StatusNotFound, "NoSuchBucket")
				return
			}
			response.WriteHeader(http.StatusOK)
		case http.MethodPut:
			putCalls++
			response.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected MinIO request: %s %s", request.Method, request.URL.String())
			response.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	store, err := NewMinioStore(context.Background(), minioEndpoint(server), "access", "secret", "skills", false)
	if err != nil || store.bucket != "skills" || headCalls != 1 || putCalls != 1 {
		t.Fatalf("NewMinioStore() = %#v, error = %v, HEAD = %d, PUT = %d", store, err, headCalls, putCalls)
	}
}

func TestNewMinioStoreAcceptsConcurrentBucketCreation(t *testing.T) {
	headCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Query().Has("location") {
			response.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(response, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		if request.Method == http.MethodHead {
			headCalls++
			if headCalls == 1 {
				writeS3Error(response, http.StatusNotFound, "NoSuchBucket")
				return
			}
			response.WriteHeader(http.StatusOK)
			return
		}
		if request.Method == http.MethodPut {
			writeS3Error(response, http.StatusConflict, "BucketAlreadyOwnedByYou")
			return
		}
		t.Errorf("unexpected MinIO request: %s %s", request.Method, request.URL.String())
		response.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	if _, err := NewMinioStore(context.Background(), minioEndpoint(server), "access", "secret", "skills", false); err != nil {
		t.Fatalf("NewMinioStore() rejected a concurrently created bucket: %v", err)
	}
}

func TestNewMinioStoreFailures(t *testing.T) {
	if _, err := NewMinioStore(context.Background(), "://invalid", "access", "secret", "skills", false); err == nil {
		t.Fatal("NewMinioStore() accepted an invalid endpoint")
	}

	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "bucket lookup",
			handler: func(response http.ResponseWriter, _ *http.Request) {
				writeS3Error(response, http.StatusForbidden, "AccessDenied")
			},
		},
		{
			name: "bucket create",
			handler: func() http.HandlerFunc {
				return func(response http.ResponseWriter, request *http.Request) {
					if request.Method == http.MethodGet && request.URL.Query().Has("location") {
						response.Header().Set("Content-Type", "application/xml")
						_, _ = io.WriteString(response, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
						return
					}
					if request.Method == http.MethodHead {
						writeS3Error(response, http.StatusNotFound, "NoSuchBucket")
						return
					}
					writeS3Error(response, http.StatusForbidden, "AccessDenied")
				}
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			if _, err := NewMinioStore(context.Background(), minioEndpoint(server), "access", "secret", "skills", false); err == nil {
				t.Fatal("NewMinioStore() did not propagate the MinIO failure")
			}
		})
	}
}

func TestMinioStoreOperationFailures(t *testing.T) {
	t.Run("ready request", func(t *testing.T) {
		store := newTestMinioStore(t, func(response http.ResponseWriter, _ *http.Request) {
			writeS3Error(response, http.StatusForbidden, "AccessDenied")
		})
		if err := store.Ready(context.Background()); err == nil {
			t.Fatal("Ready() did not propagate the MinIO failure")
		}
	})

	t.Run("ready missing bucket", func(t *testing.T) {
		store := newTestMinioStore(t, func(response http.ResponseWriter, _ *http.Request) {
			writeS3Error(response, http.StatusNotFound, "NoSuchBucket")
		})
		err := store.Ready(context.Background())
		if err == nil || err.Error() != "configured MinIO bucket does not exist" {
			t.Fatalf("Ready() error = %v", err)
		}
	})

	tests := []struct {
		name string
		run  func(*MinioStore) error
	}{
		{name: "put", run: func(store *MinioStore) error { return store.Put(context.Background(), "test.zip", []byte("archive")) }},
		{name: "get", run: func(store *MinioStore) error {
			object, err := store.Get(context.Background(), "test.zip")
			if object != nil {
				_ = object.Close()
			}
			return err
		}},
		{name: "delete", run: func(store *MinioStore) error { return store.Delete(context.Background(), "test.zip") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestMinioStore(t, func(response http.ResponseWriter, _ *http.Request) {
				writeS3Error(response, http.StatusForbidden, "AccessDenied")
			})
			if err := test.run(store); err == nil {
				t.Fatalf("%s did not propagate the MinIO failure", test.name)
			}
		})
	}
}
