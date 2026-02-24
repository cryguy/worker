package core

import (
	"encoding/base64"
	"strconv"
	"time"
)

// KVValueWithMetadata holds a value and its associated metadata.
type KVValueWithMetadata struct {
	Value    string
	Metadata *string
}

// KVListResult holds the result of a List operation with pagination info.
type KVListResult struct {
	Keys         []map[string]interface{}
	ListComplete bool
	Cursor       string // base64-encoded offset, empty when list is complete
}

// KVPair represents a key-value pair from Durable Object storage.
type KVPair struct {
	Key   string
	Value string
}

// QueueMessageInput represents a message to be sent to a queue.
type QueueMessageInput struct {
	Body        string
	ContentType string
}

// CacheEntry represents a cached HTTP response.
type CacheEntry struct {
	Status    int
	Headers   string
	Body      []byte
	ExpiresAt *time.Time
}

// R2Object holds metadata about an R2/S3-compatible object.
type R2Object struct {
	Key            string
	Size           int64
	ContentType    string
	ETag           string
	LastModified   time.Time
	CustomMetadata map[string]string
}

// R2PutOptions configures an R2 put operation.
type R2PutOptions struct {
	ContentType    string
	CustomMetadata map[string]string
}

// R2ListOptions configures an R2 list operation.
type R2ListOptions struct {
	Prefix    string
	Delimiter string
	Cursor    string
	Limit     int
}

// R2ListResult holds the result of an R2 list operation.
type R2ListResult struct {
	Objects           []R2Object
	Truncated         bool
	Cursor            string
	DelimitedPrefixes []string
}

// D1ExecResult holds the result of executing a SQL statement.
type D1ExecResult struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
	Meta    D1Meta          `json:"meta"`
}

// D1Meta holds metadata about a D1 query execution.
type D1Meta struct {
	ChangedDB   bool  `json:"changed_db"`
	Changes     int64 `json:"changes"`
	LastRowID   int64 `json:"last_row_id"`
	RowsRead    int   `json:"rows_read"`
	RowsWritten int   `json:"rows_written"`
}

// MaxKVValueSize is the maximum size of a KV value (1 MB).
const MaxKVValueSize = 1 << 20

// DecodeCursor decodes a base64-encoded cursor to an integer offset.
func DecodeCursor(cursor string) int {
	if cursor == "" {
		return 0
	}
	data, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return 0
	}
	offset, err := strconv.Atoi(string(data))
	if err != nil {
		return 0
	}
	return offset
}

// EncodeCursor encodes an integer offset to a base64 cursor string.
func EncodeCursor(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}
