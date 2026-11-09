package redissession

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
	store, err := NewStore(client, "test:session:")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.CommitCtx(ctx, "token", []byte("value"), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	value, found, err := store.FindCtx(ctx, "token")
	if err != nil || !found || string(value) != "value" {
		t.Fatalf("find = %q %t %v", value, found, err)
	}
	if err := store.DeleteCtx(ctx, "token"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindCtx(ctx, "token"); err != nil || found {
		t.Fatalf("after delete found=%t err=%v", found, err)
	}
}

func TestExpiryAndValidation(t *testing.T) {
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
	ctx := context.Background()
	if err := store.CommitCtx(ctx, "token", []byte("value"), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	server.FastForward(2 * time.Minute)
	if _, found, err := store.FindCtx(ctx, "token"); err != nil || found {
		t.Fatalf("expired found=%t err=%v", found, err)
	}
	if err := store.CommitCtx(ctx, "token", []byte("value"), time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
}
