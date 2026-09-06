package seaweed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

// TestIntegration runs against a live SeaweedFS S3 gateway:
//
//	SEAWEED_TEST_ENDPOINT=localhost:8333 SEAWEED_TEST_ACCESS_KEY=... SEAWEED_TEST_SECRET_KEY=... \
//	  go test -run Integration ./store/seaweed/
//
// Optional: SEAWEED_TEST_BUCKET (default blocksrv-test), SEAWEED_TEST_PUBLIC_URL
// (base URL of the bucket for an anonymous direct-serving check).
func TestIntegration(t *testing.T) {
	endpoint := os.Getenv("SEAWEED_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("SEAWEED_TEST_ENDPOINT not set")
	}
	bucket := os.Getenv("SEAWEED_TEST_BUCKET")
	if bucket == "" {
		bucket = "blocksrv-test"
	}
	cfg := Config{
		Endpoint:  endpoint,
		UseSSL:    os.Getenv("SEAWEED_TEST_SSL") == "true",
		AccessKey: os.Getenv("SEAWEED_TEST_ACCESS_KEY"),
		SecretKey: os.Getenv("SEAWEED_TEST_SECRET_KEY"),
		Region:    "us-east-1",
		Bucket:    bucket,
		Prefix:    fmt.Sprintf("it-%d", time.Now().UnixNano()),
		Timeout:   20 * time.Second,
	}
	ctx := context.Background()

	c, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	mc := c.store.(*minioStore).mc
	lay := c.lay
	t.Cleanup(func() {
		_ = c.Close()
		for obj := range mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: lay.prefix, Recursive: true}) {
			_ = mc.RemoveObject(ctx, bucket, obj.Key, minio.RemoveObjectOptions{})
		}
	})

	if c.GetLedgerLastBlock() != 0 {
		t.Fatalf("fresh prefix reports last round %d", c.GetLedgerLastBlock())
	}
	if err := c.PutLedgerGenesis(ctx, genesisBlob("blocksrv-it")); err != nil {
		t.Fatalf("PutLedgerGenesis: %v", err)
	}
	for r := uint64(0); r <= 5; r++ {
		if err := c.PutLedgerBlockData(ctx, r, blockBlob(r)); err != nil {
			t.Fatalf("PutLedgerBlockData(%d): %v", r, err)
		}
	}

	key, _ := lay.blockDataKey(3)
	info, err := mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		t.Fatalf("StatObject(%s): %v", key, err)
	}
	if info.ContentType != ContentTypeMsgpack {
		t.Errorf("Content-Type = %q, want %q", info.ContentType, ContentTypeMsgpack)
	}
	if cc := info.Metadata.Get("Cache-Control"); cc != CacheImmutable {
		t.Errorf("Cache-Control = %q, want %q", cc, CacheImmutable)
	}
	if got := info.UserMetadata["Round"]; got != "3" {
		t.Errorf("x-amz-meta-round = %q (all: %v), want 3", got, info.UserMetadata)
	}

	blob, closer, err := c.GetLedgerStateDelta(ctx, 3)
	if err != nil || !bytes.Equal(blob, blockBlob(3)) {
		t.Fatalf("GetLedgerStateDelta(3) mismatch: %v", err)
	}
	_ = closer.Close()
	if _, _, err := c.GetLedgerStateDelta(ctx, 3); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "latest.json", func() bool {
		obj, err := mc.GetObject(ctx, bucket, lay.latestKey(), minio.GetObjectOptions{})
		if err != nil {
			return false
		}
		defer obj.Close()
		var doc latestDoc
		if err := json.NewDecoder(obj).Decode(&doc); err != nil {
			return false
		}
		return doc.LastRound == 5 && doc.GenesisID == "blocksrv-it-v1"
	})

	// crash recovery: pointer missing, then pointer behind the data
	_ = c.Close()
	if err := mc.RemoveObject(ctx, bucket, lay.latestKey(), minio.RemoveObjectOptions{}); err != nil {
		t.Fatal(err)
	}
	c2, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if c2.GetLedgerLastBlock() != 5 {
		t.Fatalf("recovered last round = %d, want 5", c2.GetLedgerLastBlock())
	}
	// a future round long-polls until the context expires
	sctx, scancel := context.WithTimeout(ctx, 200*time.Millisecond)
	if _, _, err := c2.GetLedgerStateDelta(sctx, 9); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("future round error = %v, want DeadlineExceeded", err)
	}
	scancel()
	_ = c2.Close()

	stale, _ := json.Marshal(latestDoc{LastRound: 2})
	_, err = mc.PutObject(ctx, bucket, lay.latestKey(), bytes.NewReader(stale), int64(len(stale)), minio.PutObjectOptions{ContentType: ContentTypeJSON})
	if err != nil {
		t.Fatal(err)
	}
	c3, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	if c3.GetLedgerLastBlock() != 5 {
		t.Fatalf("healed last round = %d, want 5", c3.GetLedgerLastBlock())
	}

	if pub := os.Getenv("SEAWEED_TEST_PUBLIC_URL"); pub != "" {
		resp, err := http.Get(pub + "/" + key)
		if err != nil {
			t.Fatalf("anonymous GET: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !bytes.Equal(body, blockBlob(3)) {
			t.Fatalf("anonymous GET status %d, %d bytes", resp.StatusCode, len(body))
		}
		if ct := resp.Header.Get("Content-Type"); ct != ContentTypeMsgpack {
			t.Errorf("direct Content-Type = %q", ct)
		}
		if cc := resp.Header.Get("Cache-Control"); cc != CacheImmutable {
			t.Errorf("direct Cache-Control = %q", cc)
		}
		if resp.Header.Get("ETag") == "" {
			t.Errorf("direct response has no ETag")
		}
	}
}
