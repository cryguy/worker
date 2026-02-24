package worker

// test_bridges_test.go provides pure in-memory mock implementations of
// KVStore, DurableObjectStore, CacheStore, R2Store, and QueueSender for
// use in worker package tests. No GORM, minio, or internal/* imports.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// mockKVStore — in-memory KVStore for tests.
// ---------------------------------------------------------------------------

type kvEntry struct {
	Value     string
	Metadata  *string
	ExpiresAt *time.Time
}

type mockKVStore struct {
	mu      sync.Mutex
	entries map[string]*kvEntry
}

func newMockKVStore() *mockKVStore {
	return &mockKVStore{entries: make(map[string]*kvEntry)}
}

var _ KVStore = (*mockKVStore)(nil)

func (kv *mockKVStore) Get(key string) (*string, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	e, ok := kv.entries[key]
	if !ok {
		return nil, nil
	}
	if e.ExpiresAt != nil && e.ExpiresAt.Before(time.Now()) {
		delete(kv.entries, key)
		return nil, nil
	}
	return &e.Value, nil
}

func (kv *mockKVStore) GetWithMetadata(key string) (*KVValueWithMetadata, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	e, ok := kv.entries[key]
	if !ok {
		return nil, nil
	}
	if e.ExpiresAt != nil && e.ExpiresAt.Before(time.Now()) {
		delete(kv.entries, key)
		return nil, nil
	}
	return &KVValueWithMetadata{Value: e.Value, Metadata: e.Metadata}, nil
}

func (kv *mockKVStore) Put(key, value string, metadata *string, ttl *int) error {
	if len(value) > MaxKVValueSize {
		return fmt.Errorf("value exceeds maximum size of %d bytes", MaxKVValueSize)
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()

	var expiresAt *time.Time
	if ttl != nil && *ttl > 0 {
		t := time.Now().Add(time.Duration(*ttl) * time.Second)
		expiresAt = &t
	}

	kv.entries[key] = &kvEntry{
		Value:     value,
		Metadata:  metadata,
		ExpiresAt: expiresAt,
	}
	return nil
}

func (kv *mockKVStore) Delete(key string) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	delete(kv.entries, key)
	return nil
}

func (kv *mockKVStore) List(prefix string, limit int, cursor string) (*KVListResult, error) {
	if limit <= 0 {
		limit = 1000
	}
	offset := DecodeCursor(cursor)

	kv.mu.Lock()
	defer kv.mu.Unlock()

	// Collect non-expired keys matching prefix.
	now := time.Now()
	type item struct {
		key      string
		metadata *string
	}
	var items []item
	for k, e := range kv.entries {
		if e.ExpiresAt != nil && e.ExpiresAt.Before(now) {
			delete(kv.entries, k)
			continue
		}
		if prefix != "" && !strings.HasPrefix(k, prefix) {
			continue
		}
		items = append(items, item{key: k, metadata: e.Metadata})
	}

	// Sort by key for deterministic ordering.
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })

	// Apply offset.
	if offset > len(items) {
		offset = len(items)
	}
	items = items[offset:]

	listComplete := len(items) <= limit
	if !listComplete {
		items = items[:limit]
	}

	keys := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		entry := map[string]interface{}{
			"name": it.key,
		}
		if it.metadata != nil {
			if json.Valid([]byte(*it.metadata)) {
				entry["metadata"] = json.RawMessage(*it.metadata)
			} else {
				entry["metadata"] = *it.metadata
			}
		}
		keys = append(keys, entry)
	}

	result := &KVListResult{
		Keys:         keys,
		ListComplete: listComplete,
	}
	if !listComplete {
		result.Cursor = EncodeCursor(offset + limit)
	}
	return result, nil
}

// forceExpire sets a key's expiry into the past (test helper).
func (kv *mockKVStore) forceExpire(key string) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if e, ok := kv.entries[key]; ok {
		t := time.Now().Add(-1 * time.Second)
		e.ExpiresAt = &t
	}
}

// ---------------------------------------------------------------------------
// mockDurableObjectStore — in-memory DurableObjectStore for tests.
// ---------------------------------------------------------------------------

type mockDurableObjectStore struct {
	mu      sync.Mutex
	entries map[string]string // "namespace:objectID:key" -> valueJSON
}

func newMockDurableObjectStore() *mockDurableObjectStore {
	return &mockDurableObjectStore{entries: make(map[string]string)}
}

var _ DurableObjectStore = (*mockDurableObjectStore)(nil)

func doKey(namespace, objectID, key string) string {
	return namespace + ":" + objectID + ":" + key
}

func doPrefix(namespace, objectID string) string {
	return namespace + ":" + objectID + ":"
}

func (b *mockDurableObjectStore) Get(namespace, objectID, key string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	val, ok := b.entries[doKey(namespace, objectID, key)]
	if !ok {
		return "", nil
	}
	return val, nil
}

func (b *mockDurableObjectStore) GetMulti(namespace, objectID string, keys []string) (map[string]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make(map[string]string)
	for _, k := range keys {
		if v, ok := b.entries[doKey(namespace, objectID, k)]; ok {
			result[k] = v
		}
	}
	return result, nil
}

func (b *mockDurableObjectStore) Put(namespace, objectID, key, valueJSON string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[doKey(namespace, objectID, key)] = valueJSON
	return nil
}

func (b *mockDurableObjectStore) PutMulti(namespace, objectID string, entries map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range entries {
		b.entries[doKey(namespace, objectID, k)] = v
	}
	return nil
}

func (b *mockDurableObjectStore) Delete(namespace, objectID, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, doKey(namespace, objectID, key))
	return nil
}

func (b *mockDurableObjectStore) DeleteMulti(namespace, objectID string, keys []string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	for _, k := range keys {
		dk := doKey(namespace, objectID, k)
		if _, ok := b.entries[dk]; ok {
			delete(b.entries, dk)
			count++
		}
	}
	return count, nil
}

func (b *mockDurableObjectStore) DeleteAll(namespace, objectID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	prefix := doPrefix(namespace, objectID)
	for k := range b.entries {
		if strings.HasPrefix(k, prefix) {
			delete(b.entries, k)
		}
	}
	return nil
}

func (b *mockDurableObjectStore) List(namespace, objectID, prefix string, limit int, reverse bool) ([]KVPair, error) {
	if limit <= 0 {
		limit = 128
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	doP := doPrefix(namespace, objectID)

	type kv struct {
		key   string
		value string
	}
	var items []kv
	for k, v := range b.entries {
		if !strings.HasPrefix(k, doP) {
			continue
		}
		shortKey := k[len(doP):]
		if prefix != "" && !strings.HasPrefix(shortKey, prefix) {
			continue
		}
		items = append(items, kv{key: shortKey, value: v})
	}

	if reverse {
		sort.Slice(items, func(i, j int) bool { return items[i].key > items[j].key })
	} else {
		sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
	}

	if len(items) > limit {
		items = items[:limit]
	}

	result := make([]KVPair, len(items))
	for i, it := range items {
		result[i] = KVPair{Key: it.key, Value: it.value}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// mockR2Store — in-memory R2Store for tests.
// ---------------------------------------------------------------------------

type r2Entry struct {
	Data           []byte
	ContentType    string
	CustomMetadata map[string]string
	LastModified   time.Time
	ETag           string
}

type mockR2Store struct {
	mu      sync.Mutex
	entries map[string]*r2Entry
}

func newMockR2Store() *mockR2Store {
	return &mockR2Store{entries: make(map[string]*r2Entry)}
}

var _ R2Store = (*mockR2Store)(nil)

func (s *mockR2Store) Get(key string) ([]byte, *R2Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, nil, fmt.Errorf("key not found: %s", key)
	}
	return e.Data, &R2Object{
		Key:            key,
		Size:           int64(len(e.Data)),
		ContentType:    e.ContentType,
		ETag:           e.ETag,
		LastModified:   e.LastModified,
		CustomMetadata: e.CustomMetadata,
	}, nil
}

func (s *mockR2Store) Put(key string, data []byte, opts R2PutOptions) (*R2Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	etag := fmt.Sprintf("etag-%s-%d", key, time.Now().UnixNano())
	s.entries[key] = &r2Entry{
		Data:           data,
		ContentType:    opts.ContentType,
		CustomMetadata: opts.CustomMetadata,
		LastModified:   time.Now(),
		ETag:           etag,
	}
	return &R2Object{
		Key:            key,
		Size:           int64(len(data)),
		ContentType:    opts.ContentType,
		ETag:           etag,
		LastModified:   time.Now(),
		CustomMetadata: opts.CustomMetadata,
	}, nil
}

func (s *mockR2Store) Delete(keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.entries, k)
	}
	return nil
}

func (s *mockR2Store) Head(key string) (*R2Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return &R2Object{
		Key:            key,
		Size:           int64(len(e.Data)),
		ContentType:    e.ContentType,
		ETag:           e.ETag,
		LastModified:   e.LastModified,
		CustomMetadata: e.CustomMetadata,
	}, nil
}

func (s *mockR2Store) List(opts R2ListOptions) (*R2ListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}

	type item struct {
		key   string
		entry *r2Entry
	}
	var items []item
	for k, e := range s.entries {
		if opts.Prefix != "" && !strings.HasPrefix(k, opts.Prefix) {
			continue
		}
		if opts.Cursor != "" && k <= opts.Cursor {
			continue
		}
		items = append(items, item{key: k, entry: e})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })

	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}

	objects := make([]R2Object, len(items))
	for i, it := range items {
		objects[i] = R2Object{
			Key:          it.key,
			Size:         int64(len(it.entry.Data)),
			ETag:         it.entry.ETag,
			LastModified: it.entry.LastModified,
		}
	}

	var nextCursor string
	if truncated && len(objects) > 0 {
		nextCursor = objects[len(objects)-1].Key
	}

	return &R2ListResult{
		Objects:   objects,
		Truncated: truncated,
		Cursor:    nextCursor,
	}, nil
}

func (s *mockR2Store) PresignedGetURL(key string, expiry time.Duration) (string, error) {
	return fmt.Sprintf("https://presigned.test/bucket/%s?expires=%d", key, int(expiry.Seconds())), nil
}

func (s *mockR2Store) PublicURL(key string) (string, error) {
	return fmt.Sprintf("https://public.test/bucket/%s", key), nil
}

// ---------------------------------------------------------------------------
// nilSourceLoader is a no-op SourceLoader for tests that only use CompileAndCache.
// ---------------------------------------------------------------------------

type nilSourceLoader struct{}

func (nilSourceLoader) GetWorkerScript(siteID, deployKey string) (string, error) {
	return "", fmt.Errorf("no source loader configured in test")
}

// ---------------------------------------------------------------------------
// mockSourceLoader — returns a pre-configured script for a known site/deploy.
// ---------------------------------------------------------------------------

type mockSourceLoader struct {
	scripts map[string]string // "siteID:deployKey" -> source
}

func (m *mockSourceLoader) GetWorkerScript(siteID, deployKey string) (string, error) {
	key := siteID + ":" + deployKey
	if src, ok := m.scripts[key]; ok {
		return src, nil
	}
	return "", fmt.Errorf("no source for site %s deploy %s", siteID, deployKey)
}
