package rediscsrf

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestStoreLifecycle(t *testing.T) {
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewStore(client, "test:csrf:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Put(ctx, "id", time.Minute); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Exists(ctx, "id"); err != nil || !ok {
		t.Fatalf("exists = %t, %v", ok, err)
	}
	server.FastForward(2 * time.Minute)
	if ok, err := store.Exists(ctx, "id"); err != nil || ok {
		t.Fatalf("expired exists = %t, %v", ok, err)
	}
}

func TestValidation(t *testing.T) {
	if _, err := NewStore(nil, "p:"); err == nil {
		t.Fatal("nil client accepted")
	}
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	if _, err := NewStore(client, " "); err == nil {
		t.Fatal("empty prefix accepted")
	}
	store, err := NewStore(client, "p:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "id", 0); err == nil {
		t.Fatal("zero TTL accepted")
	}
}
