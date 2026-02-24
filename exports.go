package worker

import "github.com/cryguy/worker/internal/core"

// Type aliases re-exporting internal/core types so downstream code
// can use worker.WorkerRequest, worker.Env, etc. without importing
// the internal package directly.

type WorkerRequest = core.WorkerRequest
type WorkerResponse = core.WorkerResponse
type WorkerResult = core.WorkerResult
type LogEntry = core.LogEntry
type TailEvent = core.TailEvent
type Env = core.Env
type EngineConfig = core.EngineConfig
type SourceLoader = core.SourceLoader
type WorkerDispatcher = core.WorkerDispatcher
type KVStore = core.KVStore
type CacheStore = core.CacheStore
type CacheEntry = core.CacheEntry
type DurableObjectStore = core.DurableObjectStore
type QueueSender = core.QueueSender
type R2Store = core.R2Store
type D1Store = core.D1Store
type EnvBindingFunc = core.EnvBindingFunc
type ServiceBindingConfig = core.ServiceBindingConfig
type AssetsFetcher = core.AssetsFetcher
type JSRuntime = core.JSRuntime
type KVValueWithMetadata = core.KVValueWithMetadata
type KVListResult = core.KVListResult
type KVPair = core.KVPair
type QueueMessageInput = core.QueueMessageInput
type R2Object = core.R2Object
type R2PutOptions = core.R2PutOptions
type R2ListOptions = core.R2ListOptions
type R2ListResult = core.R2ListResult
type D1ExecResult = core.D1ExecResult
type D1Meta = core.D1Meta
type CryptoKeyEntry = core.CryptoKeyEntry
type WebSocketBridger = core.WebSocketBridger

// Constants re-exported from core.
const MaxKVValueSize = core.MaxKVValueSize

// Functions re-exported from core.
var DecodeCursor = core.DecodeCursor
var EncodeCursor = core.EncodeCursor
