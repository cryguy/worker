package core

import "time"

// SourceLoader retrieves worker JS source code.
type SourceLoader interface {
	GetWorkerScript(siteID, deployKey string) (string, error)
}

// WorkerDispatcher executes a worker (used by service bindings to dispatch
// to other workers without a direct Engine dependency).
type WorkerDispatcher interface {
	Execute(siteID, deployKey string, env *Env, req *WorkerRequest) *WorkerResult
}

// KVStore backs a single KV namespace.
type KVStore interface {
	Get(key string) (*string, error)
	GetWithMetadata(key string) (*KVValueWithMetadata, error)
	Put(key, value string, metadata *string, ttl *int) error
	Delete(key string) error
	List(prefix string, limit int, cursor string) (*KVListResult, error)
}

// CacheStore backs the Cache API (site-scoped).
type CacheStore interface {
	Match(cacheName, url string) (*CacheEntry, error)
	Put(cacheName, url string, status int, headers string, body []byte, ttl *int) error
	Delete(cacheName, url string) (bool, error)
}

// DurableObjectStore backs Durable Object storage.
type DurableObjectStore interface {
	Get(namespace, objectID, key string) (string, error)
	GetMulti(namespace, objectID string, keys []string) (map[string]string, error)
	Put(namespace, objectID, key, valueJSON string) error
	PutMulti(namespace, objectID string, entries map[string]string) error
	Delete(namespace, objectID, key string) error
	DeleteMulti(namespace, objectID string, keys []string) (int, error)
	DeleteAll(namespace, objectID string) error
	List(namespace, objectID, prefix string, limit int, reverse bool) ([]KVPair, error)
}

// QueueSender backs queue message production for a single queue.
type QueueSender interface {
	Send(body, contentType string) (string, error)
	SendBatch(messages []QueueMessageInput) ([]string, error)
}

// D1Store backs a single D1 database binding.
type D1Store interface {
	Exec(sql string, bindings []interface{}) (*D1ExecResult, error)
	Close() error
}

// R2Store backs R2-compatible object storage for a single bucket.
type R2Store interface {
	Get(key string) ([]byte, *R2Object, error)
	Put(key string, data []byte, opts R2PutOptions) (*R2Object, error)
	Delete(keys []string) error
	Head(key string) (*R2Object, error)
	List(opts R2ListOptions) (*R2ListResult, error)
	PresignedGetURL(key string, expiry time.Duration) (string, error)
	PublicURL(key string) (string, error)
}
