package kv

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMemoryStoreGetSetUpdateDelete(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	type value struct {
		Name string `json:"name"`
		Seen bool   `json:"seen"`
	}
	if err := store.Set(ctx, "k", value{Name: "session"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var got value
	found, err := store.Get(ctx, "k", &got)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.Name != "session" {
		t.Fatalf("unexpected get result found=%t got=%+v", found, got)
	}
	updated, err := store.Update(ctx, "k", time.Minute, func(raw []byte) ([]byte, error) {
		var v value
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		v.Seen = true
		return json.Marshal(v)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated {
		t.Fatal("expected update to find key")
	}
	found, err = store.Get(ctx, "k", &got)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !got.Seen {
		t.Fatalf("expected updated value, found=%t got=%+v", found, got)
	}
	if err := store.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	found, err = store.Get(ctx, "k", &got)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected deleted key to be absent")
	}
}

func TestMemoryStoreExpiresKeys(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	if err := store.Set(ctx, "k", map[string]string{"v": "1"}, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	var got map[string]string
	found, err := store.Get(ctx, "k", &got)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected key to expire")
	}
}

func TestNewStoreCreatesRedisStore(t *testing.T) {
	store, err := NewStore("redis://localhost:6379/0")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.(*RedisStore); !ok {
		t.Fatalf("expected RedisStore, got %T", store)
	}
}
