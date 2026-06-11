package kv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Store interface {
	Get(ctx context.Context, key string, dest any) (bool, error)
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Update(ctx context.Context, key string, ttl time.Duration, update func([]byte) ([]byte, error)) (bool, error)
	Delete(ctx context.Context, key string) error
}

type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]memoryItem
}

type RedisStore struct {
	client *redis.Client
}

type memoryItem struct {
	value     []byte
	expiresAt time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]memoryItem)}
}

func NewStore(rawURL string) (Store, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || strings.EqualFold(rawURL, "memory://") || strings.EqualFold(rawURL, "memory") {
		return NewMemoryStore(), nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	switch parsed.Scheme {
	case "redis", "rediss":
		opts, err := redis.ParseURL(rawURL)
		if err != nil {
			return nil, err
		}
		return NewRedisStore(opts), nil
	default:
		return nil, fmt.Errorf("unsupported kv url scheme %q", parsed.Scheme)
	}
}

func NewRedisStore(opts *redis.Options) *RedisStore {
	return &RedisStore{client: redis.NewClient(opts)}
}

func (s *MemoryStore) Get(_ context.Context, key string, dest any) (bool, error) {
	s.mu.RLock()
	item, ok := s.items[key]
	s.mu.RUnlock()
	if !ok || item.expired(time.Now()) {
		if ok {
			_ = s.Delete(context.Background(), key)
		}
		return false, nil
	}
	if err := json.Unmarshal(item.value, dest); err != nil {
		return false, err
	}
	return true, nil
}

func (s *MemoryStore) Set(_ context.Context, key string, value any, ttl time.Duration) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.items[key] = memoryItem{value: payload, expiresAt: expiresAt(ttl)}
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Update(_ context.Context, key string, ttl time.Duration, update func([]byte) ([]byte, error)) (bool, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[key]
	if !ok || item.expired(now) {
		if ok {
			delete(s.items, key)
		}
		return false, nil
	}
	payload, err := update(append([]byte(nil), item.value...))
	if err != nil {
		return false, err
	}
	item.value = payload
	if ttl > 0 {
		item.expiresAt = expiresAt(ttl)
	}
	s.items[key] = item
	return true, nil
}

func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.items, key)
	s.mu.Unlock()
	return nil
}

func (i memoryItem) expired(now time.Time) bool {
	return !i.expiresAt.IsZero() && now.After(i.expiresAt)
}

func expiresAt(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return time.Now().UTC().Add(ttl)
}

func (s *RedisStore) Get(ctx context.Context, key string, dest any) (bool, error) {
	payload, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		return false, err
	}
	return true, nil
}

func (s *RedisStore) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, key, payload, ttl).Err()
}

func (s *RedisStore) Update(ctx context.Context, key string, ttl time.Duration, update func([]byte) ([]byte, error)) (bool, error) {
	for {
		found := true
		err := s.client.Watch(ctx, func(tx *redis.Tx) error {
			payload, err := tx.Get(ctx, key).Bytes()
			if errors.Is(err, redis.Nil) {
				found = false
				return nil
			}
			if err != nil {
				return err
			}
			updated, err := update(payload)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				expiration := ttl
				if expiration <= 0 {
					expiration = redis.KeepTTL
				}
				pipe.Set(ctx, key, updated, expiration)
				return nil
			})
			return err
		}, key)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		return found, err
	}
}

func (s *RedisStore) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}
