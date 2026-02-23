package worker

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/andybalholm/brotli"
	"modernc.org/quickjs"
)

const maxDecompressedSize = 128 * 1024 * 1024 // 128 MB

// compressStreamState holds the Go-side state for one streaming compressor or
// decompressor. For compression the writer writes compressed chunks to buf.
// For decompression an io.Pipe feeds a background goroutine that runs the
// decompressor, producing decompressed output incrementally per chunk.
type compressStreamState struct {
	format string
	mode   string // "compress" or "decompress"

	// Compression state: writer writes compressed data into buf.
	buf    bytes.Buffer
	writer io.WriteCloser

	// Streaming decompression state.
	decompPW   *io.PipeWriter
	decompMu   sync.Mutex
	decompOut  bytes.Buffer
	decompErr  error
	decompDone chan struct{} // closed when goroutine exits
}

// compressionJS implements CompressionStream and DecompressionStream.
// Each chunk is sent to Go-backed functions for real streaming compression.
const compressionJS = `
(function() {

// Helper: convert base64 to Uint8Array (needed for binary stream output).
function __b64ToUint8Array(b64) {
	var _b64e = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
	var _b64d = new Uint8Array(128);
	for (var i = 0; i < _b64e.length; i++) _b64d[_b64e.charCodeAt(i)] = i;

	var pad = 0;
	if (b64.length > 0 && b64[b64.length - 1] === '=') pad++;
	if (b64.length > 1 && b64[b64.length - 2] === '=') pad++;
	var outLen = (b64.length * 3 / 4) - pad;
	var out = new Uint8Array(outLen);
	var j = 0;
	for (var i = 0; i < b64.length; i += 4) {
		var a = _b64d[b64.charCodeAt(i)];
		var b = _b64d[b64.charCodeAt(i + 1)];
		var c = _b64d[b64.charCodeAt(i + 2)];
		var d = _b64d[b64.charCodeAt(i + 3)];
		out[j++] = (a << 2) | (b >> 4);
		if (j < outLen) out[j++] = ((b & 15) << 4) | (c >> 2);
		if (j < outLen) out[j++] = ((c & 3) << 6) | d;
	}
	return out;
}

function __chunkToUint8Array(chunk) {
	if (typeof chunk === 'string') {
		return new TextEncoder().encode(chunk);
	} else if (chunk instanceof ArrayBuffer) {
		return new Uint8Array(chunk);
	} else if (ArrayBuffer.isView(chunk)) {
		return new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
	} else {
		return new TextEncoder().encode(String(chunk));
	}
}

class CompressionStream {
	constructor(format) {
		if (format !== 'gzip' && format !== 'deflate' && format !== 'deflate-raw' && format !== 'br') {
			throw new TypeError('Unsupported compression format: ' + format);
		}
		var reqID = String(globalThis.__requestID);
		var streamID = __compressInit(reqID, format);
		var ts = new TransformStream({
			transform(chunk, controller) {
				var data = __chunkToUint8Array(chunk);
				var resultB64 = __compressChunk(reqID, streamID, __bufferSourceToB64(data));
				if (resultB64.length > 0) {
					controller.enqueue(__b64ToUint8Array(resultB64));
				}
			},
			flush(controller) {
				var resultB64 = __compressFlush(reqID, streamID);
				if (resultB64.length > 0) {
					controller.enqueue(__b64ToUint8Array(resultB64));
				}
			}
		});
		this.readable = ts.readable;
		this.writable = ts.writable;
	}
}

class DecompressionStream {
	constructor(format) {
		if (format !== 'gzip' && format !== 'deflate' && format !== 'deflate-raw' && format !== 'br') {
			throw new TypeError('Unsupported compression format: ' + format);
		}
		var reqID = String(globalThis.__requestID);
		var streamID = __decompressInit(reqID, format);
		var ts = new TransformStream({
			transform(chunk, controller) {
				var data = __chunkToUint8Array(chunk);
				var resultB64 = __decompressChunk(reqID, streamID, __bufferSourceToB64(data));
				if (resultB64.length > 0) {
					controller.enqueue(__b64ToUint8Array(resultB64));
				}
			},
			flush(controller) {
				var resultB64 = __decompressFlush(reqID, streamID);
				if (resultB64.length > 0) {
					controller.enqueue(__b64ToUint8Array(resultB64));
				}
			}
		});
		this.readable = ts.readable;
		this.writable = ts.writable;
	}
}

globalThis.CompressionStream = CompressionStream;
globalThis.DecompressionStream = DecompressionStream;

})();
`

// newCompressWriter creates a compression writer for the given format.
func newCompressWriter(buf *bytes.Buffer, format string) (io.WriteCloser, error) {
	switch format {
	case "gzip":
		return gzip.NewWriter(buf), nil
	case "deflate", "deflate-raw":
		return flate.NewWriter(buf, flate.DefaultCompression)
	case "br":
		return brotli.NewWriter(buf), nil
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}

// setupCompression registers Go-backed streaming compress/decompress functions
// and evaluates the JS classes. Must run after setupStreams and setupCrypto.
func setupCompression(vm *quickjs.VM, _ *eventLoop) error {

	// --- Legacy bulk functions (kept for backward compat with direct callers) ---

	// __compress(format, dataB64) -> compressedB64
	err := registerGoFunc(vm, "__compress", func(format, dataB64 string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("compress: invalid base64")
		}

		var buf bytes.Buffer
		w, err := newCompressWriter(&buf, format)
		if err != nil {
			return "", fmt.Errorf("compress: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return "", fmt.Errorf("compress: %w", err)
		}
		if err := w.Close(); err != nil {
			return "", fmt.Errorf("compress: %w", err)
		}

		return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __compress: %w", err)
	}

	// __decompress(format, dataB64) -> decompressedB64
	err = registerGoFunc(vm, "__decompress", func(format, dataB64 string) (string, error) {
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("decompress: invalid base64")
		}

		var result []byte
		switch format {
		case "gzip":
			r, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				return "", fmt.Errorf("decompress: %w", err)
			}
			result, err = io.ReadAll(io.LimitReader(r, int64(maxDecompressedSize)+1))
			if err != nil {
				return "", fmt.Errorf("decompress: %w", err)
			}
			if len(result) > maxDecompressedSize {
				return "", fmt.Errorf("decompress: output exceeds maximum allowed size")
			}
			_ = r.Close()
		case "deflate", "deflate-raw":
			r := flate.NewReader(bytes.NewReader(data))
			result, err = io.ReadAll(io.LimitReader(r, int64(maxDecompressedSize)+1))
			if err != nil {
				return "", fmt.Errorf("decompress: %w", err)
			}
			if len(result) > maxDecompressedSize {
				return "", fmt.Errorf("decompress: output exceeds maximum allowed size")
			}
			_ = r.Close()
		case "br":
			r := brotli.NewReader(bytes.NewReader(data))
			result, err = io.ReadAll(io.LimitReader(r, int64(maxDecompressedSize)+1))
			if err != nil {
				return "", fmt.Errorf("decompress: %w", err)
			}
			if len(result) > maxDecompressedSize {
				return "", fmt.Errorf("decompress: output exceeds maximum allowed size")
			}
		default:
			return "", fmt.Errorf("decompress: unsupported format %q", format)
		}

		return base64.StdEncoding.EncodeToString(result), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __decompress: %w", err)
	}

	// --- Streaming compression functions ---

	// __compressInit(requestID, format) -> streamID
	err = registerGoFunc(vm, "__compressInit", func(requestIDStr, format string) (string, error) {
		reqID := parseReqID(requestIDStr)
		state := getRequestState(reqID)
		if state == nil {
			return "", fmt.Errorf("compressInit: invalid request state")
		}

		cs := &compressStreamState{format: format, mode: "compress"}
		w, err := newCompressWriter(&cs.buf, format)
		if err != nil {
			return "", fmt.Errorf("compressInit: %w", err)
		}
		cs.writer = w

		if state.compressStreams == nil {
			state.compressStreams = make(map[string]*compressStreamState)
		}
		state.nextCompressID++
		streamID := strconv.FormatInt(state.nextCompressID, 10)
		state.compressStreams[streamID] = cs

		return streamID, nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __compressInit: %w", err)
	}

	// __compressChunk(requestID, streamID, base64data) -> base64 compressed output
	err = registerGoFunc(vm, "__compressChunk", func(requestIDStr, streamID, dataB64 string) (string, error) {
		reqID := parseReqID(requestIDStr)
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("compressChunk: invalid base64")
		}

		state := getRequestState(reqID)
		if state == nil || state.compressStreams == nil {
			return "", fmt.Errorf("compressChunk: invalid state")
		}
		cs, ok := state.compressStreams[streamID]
		if !ok {
			return "", fmt.Errorf("compressChunk: unknown stream")
		}

		// Reset buf to capture only this chunk's compressed output.
		cs.buf.Reset()
		if _, err := cs.writer.Write(data); err != nil {
			return "", fmt.Errorf("compressChunk: %w", err)
		}

		return base64.StdEncoding.EncodeToString(cs.buf.Bytes()), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __compressChunk: %w", err)
	}

	// __compressFlush(requestID, streamID) -> base64 remaining compressed data
	err = registerGoFunc(vm, "__compressFlush", func(requestIDStr, streamID string) (string, error) {
		reqID := parseReqID(requestIDStr)
		state := getRequestState(reqID)
		if state == nil || state.compressStreams == nil {
			return "", fmt.Errorf("compressFlush: invalid state")
		}
		cs, ok := state.compressStreams[streamID]
		if !ok {
			return "", fmt.Errorf("compressFlush: unknown stream")
		}

		// Reset buf, then close the writer to flush final compressed data.
		cs.buf.Reset()
		if err := cs.writer.Close(); err != nil {
			return "", fmt.Errorf("compressFlush: %w", err)
		}
		delete(state.compressStreams, streamID)

		return base64.StdEncoding.EncodeToString(cs.buf.Bytes()), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __compressFlush: %w", err)
	}

	// --- Streaming decompression functions ---

	// __decompressInit(requestID, format) -> streamID
	err = registerGoFunc(vm, "__decompressInit", func(requestIDStr, format string) (string, error) {
		reqID := parseReqID(requestIDStr)
		state := getRequestState(reqID)
		if state == nil {
			return "", fmt.Errorf("decompressInit: invalid request state")
		}

		pr, pw := io.Pipe()
		cs := &compressStreamState{
			format:     format,
			mode:       "decompress",
			decompPW:   pw,
			decompDone: make(chan struct{}),
		}

		// Spawn goroutine that reads from the pipe through a decompressor
		// and accumulates decompressed output in cs.decompOut.
		go func() {
			defer close(cs.decompDone)
			defer func() { _ = pr.Close() }()

			var reader io.ReadCloser
			switch format {
			case "gzip":
				r, err := gzip.NewReader(pr)
				if err != nil {
					cs.decompMu.Lock()
					cs.decompErr = err
					cs.decompMu.Unlock()
					return
				}
				reader = r
			case "deflate", "deflate-raw":
				reader = flate.NewReader(pr)
			case "br":
				reader = io.NopCloser(brotli.NewReader(pr))
			default:
				cs.decompMu.Lock()
				cs.decompErr = fmt.Errorf("unsupported format %q", format)
				cs.decompMu.Unlock()
				return
			}
			defer func() { _ = reader.Close() }()

			buf := make([]byte, 32*1024)
			for {
				n, err := reader.Read(buf)
				if n > 0 {
					cs.decompMu.Lock()
					cs.decompOut.Write(buf[:n])
					cs.decompMu.Unlock()
				}
				if err != nil {
					if err != io.EOF {
						cs.decompMu.Lock()
						cs.decompErr = err
						cs.decompMu.Unlock()
					}
					return
				}
			}
		}()

		if state.compressStreams == nil {
			state.compressStreams = make(map[string]*compressStreamState)
		}
		state.nextCompressID++
		streamID := strconv.FormatInt(state.nextCompressID, 10)
		state.compressStreams[streamID] = cs

		return streamID, nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __decompressInit: %w", err)
	}

	// __decompressChunk(requestID, streamID, base64data) -> base64 decompressed output
	// Feeds compressed data to the decompressor goroutine via io.Pipe and
	// returns any decompressed output that is already available.
	err = registerGoFunc(vm, "__decompressChunk", func(requestIDStr, streamID, dataB64 string) (string, error) {
		reqID := parseReqID(requestIDStr)
		data, err := base64.StdEncoding.DecodeString(dataB64)
		if err != nil {
			return "", fmt.Errorf("decompressChunk: invalid base64")
		}

		state := getRequestState(reqID)
		if state == nil || state.compressStreams == nil {
			return "", fmt.Errorf("decompressChunk: invalid state")
		}
		cs, ok := state.compressStreams[streamID]
		if !ok {
			return "", fmt.Errorf("decompressChunk: unknown stream")
		}

		// Feed compressed data to the decompressor goroutine. Write in a
		// sub-goroutine because PipeWriter.Write blocks until the reader
		// consumes the data.
		errCh := make(chan error, 1)
		go func() {
			_, werr := cs.decompPW.Write(data)
			errCh <- werr
		}()

		if werr := <-errCh; werr != nil {
			return "", fmt.Errorf("decompressChunk: %w", werr)
		}

		// Collect any decompressed output the goroutine has produced so far.
		cs.decompMu.Lock()
		out := make([]byte, cs.decompOut.Len())
		copy(out, cs.decompOut.Bytes())
		cs.decompOut.Reset()
		derr := cs.decompErr
		cs.decompMu.Unlock()

		if derr != nil {
			return "", fmt.Errorf("decompressChunk: %w", derr)
		}

		return base64.StdEncoding.EncodeToString(out), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __decompressChunk: %w", err)
	}

	// __decompressFlush(requestID, streamID) -> base64 remaining decompressed data
	err = registerGoFunc(vm, "__decompressFlush", func(requestIDStr, streamID string) (string, error) {
		reqID := parseReqID(requestIDStr)
		state := getRequestState(reqID)
		if state == nil || state.compressStreams == nil {
			return "", fmt.Errorf("decompressFlush: invalid state")
		}
		cs, ok := state.compressStreams[streamID]
		if !ok {
			return "", fmt.Errorf("decompressFlush: unknown stream")
		}

		// Close the pipe writer to signal EOF to the decompressor goroutine,
		// then wait for it to finish processing all remaining data.
		_ = cs.decompPW.Close()
		<-cs.decompDone

		cs.decompMu.Lock()
		result := make([]byte, cs.decompOut.Len())
		copy(result, cs.decompOut.Bytes())
		cs.decompOut.Reset()
		derr := cs.decompErr
		cs.decompMu.Unlock()

		delete(state.compressStreams, streamID)

		if derr != nil {
			return "", fmt.Errorf("decompressFlush: %w", derr)
		}

		return base64.StdEncoding.EncodeToString(result), nil
	}, false)
	if err != nil {
		return fmt.Errorf("registering __decompressFlush: %w", err)
	}

	if err := evalDiscard(vm, compressionJS); err != nil {
		return fmt.Errorf("evaluating compression.js: %w", err)
	}
	return nil
}
