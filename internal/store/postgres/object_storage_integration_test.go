//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/benemon/dufflebag/internal/store/objectstore"
	store "github.com/benemon/dufflebag/internal/store/postgres"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Ceph rather than a lighter stand-in: this is the object store the lab
// actually runs, and RGW's quirks — path-style addressing, a user that must be
// created rather than inherited — are the ones worth meeting in a test.
const cephImage = "quay.io/benjamin_holmes/ceph-aio:v20"

func openTestObjectStore(t *testing.T) (objectstore.Config, *objectstore.Store) {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        cephImage,
			ExposedPorts: []string{"8000/tcp"},
			// The image carries its own healthcheck, which is the only thing
			// that knows when Ceph is ready. Probing the port is not enough
			// and fails two ways: the connection is accepted before RGW
			// listens, and RGW serves HTTP before the cluster can run an
			// admin command such as creating the user below.
			WaitingFor: wait.ForHealthCheck().WithStartupTimeout(5 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start Ceph: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate Ceph: %v", err)
		}
	})
	port, err := container.MappedPort(ctx, "8000/tcp")
	if err != nil {
		t.Fatalf("Ceph mapped port: %v", err)
	}
	// RGW has no root credential to inherit, so the user is created rather than
	// configured. The bucket itself is made through the S3 client the code uses,
	// so nothing here depends on a CLI being present in the image.
	exitCode, output, err := container.Exec(ctx, []string{
		"radosgw-admin", "user", "create", "--uid=dufflebag-test",
		"--display-name=dufflebag test", "--access-key=testaccess", "--secret-key=testsecret",
	})
	if err != nil || exitCode != 0 {
		var data []byte
		if output != nil {
			data, _ = io.ReadAll(output)
		}
		t.Fatalf("provision Ceph user: exit %d, %v: %s", exitCode, err, data)
	}
	config := objectstore.Config{
		Endpoint: fmt.Sprintf("http://127.0.0.1:%s", port.Port()),
		Region:   "us-east-1", Bucket: "dufflebag-test",
		AccessKey: "testaccess", SecretKey: "testsecret",
	}
	// The deployment provisions the bucket in production, so the code under
	// test only ever checks for one. The test therefore makes it here rather
	// than dufflebag growing a create path it would never use.
	createTestBucket(t, config)

	objects, err := objectstore.New(config)
	if err != nil {
		t.Fatal(err)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := objects.CheckBucket(checkCtx); err != nil {
		t.Fatalf("check Ceph bucket: %v", err)
	}
	return config, objects
}

func createTestBucket(t *testing.T, config objectstore.Config) {
	t.Helper()
	client := s3.NewFromConfig(aws.Config{
		Region:       config.Region,
		BaseEndpoint: aws.String(config.Endpoint),
		Credentials: credentials.NewStaticCredentialsProvider(
			config.AccessKey, config.SecretKey, "",
		),
		HTTPClient: http.DefaultClient,
	}, func(options *s3.Options) { options.UsePathStyle = true })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &config.Bucket}); err != nil {
		t.Fatalf("create Ceph bucket %q: %v", config.Bucket, err)
	}
}

// Object storage credentials arrive in the environment and stay in memory. The
// schema is where they would end up if that ever changed, so the schema is
// where this asks: a table holding them must not exist, and a column named for
// one must not appear anywhere.
func TestObjectStorageCredentialsHaveNoHomeInTheSchema(t *testing.T) {
	db, _, cleanup := openTestDatabase(t)
	defer cleanup()
	ctx := context.Background()
	objects, err := objectstore.New(objectstore.Config{
		Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", Bucket: "sboms",
		AccessKey: "must-not-persist", SecretKey: "must-not-persist",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = store.NewRepositoryWithObjectStore(db, objects)

	var offending string
	if err := db.QueryRowContext(ctx, `
		SELECT coalesce(string_agg(table_name || '.' || column_name, ', '), '')
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND (column_name IN ('secret_key', 'access_key')
		       OR table_name = 'object_storage_configuration')
	`).Scan(&offending); err != nil {
		t.Fatal(err)
	}
	if offending != "" {
		t.Fatalf("schema can hold object storage credentials: %s", offending)
	}
}
