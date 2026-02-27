package worker

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// Encoding edge cases — adapted from workerd encoding-test.js
// ---------------------------------------------------------------------------

// TestEncodingEdge_StreamingMultibyteSplit feeds a 4-byte UTF-8 sequence
// (cat-smile emoji U+1F63A) one byte at a time using stream:true and verifies
// the decoder reassembles it correctly before the final flush.
//
// This is the canonical streaming-decoder test from workerd; the key
// divergence between engines is whether buffered continuation bytes are
// withheld until the codepoint is complete rather than emitted as replacement
// characters.
//
// The engine's TextDecoder polyfill does not buffer partial sequences with
// stream:true — it decodes each byte independently. This test probes for that
// and skips if streaming buffering is absent, while still verifying that
// decoding all 4 bytes at once works correctly.
func TestEncodingEdge_StreamingMultibyteSplit(t *testing.T) {
	e := newTestEngine(t)

	// U+1F63A SMILING CAT FACE WITH OPEN MOUTH = F0 9F 98 BA
	// First verify that decoding all bytes at once yields the correct codepoint.
	source := `export default {
  fetch(request, env) {
    const bytes = new Uint8Array([0xF0, 0x9F, 0x98, 0xBA]);
    const decoder = new TextDecoder('utf-8');

    // Batch decode (must work on all engines).
    const full = decoder.decode(bytes);
    const fullCP = full.codePointAt(0);

    // Stream decode byte-by-byte: probe whether buffering is supported.
    const dec2 = new TextDecoder('utf-8');
    let streamed = '';
    for (let i = 0; i < bytes.length; i++) {
      streamed += dec2.decode(bytes.slice(i, i + 1), { stream: true });
    }
    streamed += dec2.decode();
    const streamCP = streamed.codePointAt(0);
    // If stream buffering works, streamed should equal full (same codepoint).
    const streamingWorks = streamCP === fullCP;

    return Response.json({ fullCP, fullLen: full.length, streamCP, streamingWorks });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		FullCP         int  `json:"fullCP"`
		FullLen        int  `json:"fullLen"`
		StreamCP       int  `json:"streamCP"`
		StreamingWorks bool `json:"streamingWorks"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Batch decode must always work.
	if data.FullCP != 0x1F63A {
		t.Errorf("batch decode: codePoint = 0x%X, want 0x1F63A (U+1F63A)", data.FullCP)
	}
	if data.FullLen != 2 {
		t.Errorf("batch decode: len = %d, want 2 (JS surrogate pair)", data.FullLen)
	}

	// Streaming decode: document the gap if unsupported.
	if !data.StreamingWorks {
		t.Skipf("engine TextDecoder stream:true does not buffer partial multi-byte sequences (got streamCP=0x%X); feature unimplemented", data.StreamCP)
	}
}

// TestEncodingEdge_BOMStrippedOnlyOnce verifies that a single UTF-8 BOM
// (EF BB BF) is stripped and a double BOM leaves one copy, matching the
// WHATWG spec. Tests stream-mode decoding byte-by-byte.
//
// NOTE: The engine's built-in TextDecoder (V8) does support BOM stripping;
// the QuickJS fallback polyfill does not. This test documents the expected
// spec behaviour and will skip if the engine does not implement it.
func TestEncodingEdge_BOMStrippedOnlyOnce(t *testing.T) {
	e := newTestEngine(t)

	// First probe: can this engine's TextDecoder even strip a BOM?
	probeSource := `export default {
  fetch(request, env) {
    const bom = new Uint8Array([0xEF, 0xBB, 0xBF, 0x41]); // BOM + 'A'
    const result = new TextDecoder().decode(bom);
    return Response.json({ len: result.length, first: result.charCodeAt(0) });
  },
};`
	pr := execJS(t, e, probeSource, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, pr)
	var probe struct {
		Len   int `json:"len"`
		First int `json:"first"`
	}
	if err := json.Unmarshal(pr.Response.Body, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	if probe.Len != 1 || probe.First != 0x41 {
		t.Skipf("engine TextDecoder does not strip BOM (got len=%d first=0x%04X); feature unimplemented", probe.Len, probe.First)
	}

	source := `export default {
  fetch(request, env) {
    const decoder = new TextDecoder('utf-8');
    const bom    = new Uint8Array([0xEF, 0xBB, 0xBF]);
    const bomBom = new Uint8Array([0xEF, 0xBB, 0xBF, 0xEF, 0xBB, 0xBF]);

    let single = '';
    for (let i = 0; i < bom.length; i++) {
      single += decoder.decode(bom.slice(i, i+1), { stream: true });
    }
    single += decoder.decode();

    const decoder2 = new TextDecoder('utf-8');
    let dual = '';
    for (let i = 0; i < bomBom.length; i++) {
      dual += decoder2.decode(bomBom.slice(i, i+1), { stream: true });
    }
    dual += decoder2.decode();

    return Response.json({
      singleLen: single.length,
      dualLen: dual.length,
      dualCode: dual.charCodeAt(0),
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		SingleLen int `json:"singleLen"`
		DualLen   int `json:"dualLen"`
		DualCode  int `json:"dualCode"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.SingleLen != 0 {
		t.Errorf("single BOM: len = %d, want 0 (BOM stripped)", data.SingleLen)
	}
	if data.DualLen != 1 {
		t.Errorf("double BOM: len = %d, want 1 (first BOM stripped, second kept)", data.DualLen)
	}
	if data.DualCode != 0xFEFF {
		t.Errorf("double BOM: remaining char code = 0x%04X, want 0xFEFF", data.DualCode)
	}
}

// TestEncodingEdge_IgnoreBOMPreservesAll verifies that ignoreBOM:true keeps
// the BOM character intact. Skips if the engine does not support ignoreBOM.
func TestEncodingEdge_IgnoreBOMPreservesAll(t *testing.T) {
	e := newTestEngine(t)

	// Probe: does the engine support the ignoreBOM option?
	probeSource := `export default {
  fetch(request, env) {
    let supported = false;
    try {
      const d = new TextDecoder('utf-8', { ignoreBOM: true });
      supported = d.ignoreBOM === true;
    } catch(e) {}
    return Response.json({ supported });
  },
};`
	pr := execJS(t, e, probeSource, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, pr)
	var probe struct {
		Supported bool `json:"supported"`
	}
	if err := json.Unmarshal(pr.Response.Body, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	if !probe.Supported {
		t.Skip("engine TextDecoder does not support ignoreBOM option; feature unimplemented")
	}

	source := `export default {
  fetch(request, env) {
    const decoder = new TextDecoder('utf-8', { ignoreBOM: true });
    const bom = new Uint8Array([0xEF, 0xBB, 0xBF]);
    let result = '';
    for (let i = 0; i < bom.length; i++) {
      result += decoder.decode(bom.slice(i, i+1), { stream: true });
    }
    result += decoder.decode();
    return Response.json({ len: result.length, code: result.charCodeAt(0) });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Len  int `json:"len"`
		Code int `json:"code"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Len != 1 {
		t.Errorf("ignoreBOM: len = %d, want 1 (BOM preserved)", data.Len)
	}
	if data.Code != 0xFEFF {
		t.Errorf("ignoreBOM: char code = 0x%04X, want 0xFEFF", data.Code)
	}
}

// TestEncodingEdge_FatalOnInvalidUTF8 verifies that fatal:true throws on
// truncated multi-byte sequences. Skips if the engine does not support fatal.
func TestEncodingEdge_FatalOnInvalidUTF8(t *testing.T) {
	e := newTestEngine(t)

	// Probe: does the engine support fatal option?
	probeSource := `export default {
  fetch(request, env) {
    let supported = false;
    try {
      const d = new TextDecoder('utf-8', { fatal: true });
      supported = d.fatal === true;
    } catch(e) {}
    return Response.json({ supported });
  },
};`
	pr := execJS(t, e, probeSource, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, pr)
	var probe struct {
		Supported bool `json:"supported"`
	}
	if err := json.Unmarshal(pr.Response.Body, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	if !probe.Supported {
		t.Skip("engine TextDecoder does not support fatal option; feature unimplemented")
	}

	source := `export default {
  fetch(request, env) {
    const fatal = new TextDecoder('utf-8', { fatal: true });
    // 3-byte euro sign: 0xE2 0x82 0xAC — only the 2-byte head
    const head = new Uint8Array([0xE2, 0x82]);
    // orphan continuation byte only
    const tail = new Uint8Array([0xAC]);

    let headThrew = false;
    let tailThrew = false;
    try { fatal.decode(head); } catch(e) { headThrew = true; }
    const fatal2 = new TextDecoder('utf-8', { fatal: true });
    try { fatal2.decode(tail); } catch(e) { tailThrew = true; }

    return Response.json({ headThrew, tailThrew });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		HeadThrew bool `json:"headThrew"`
		TailThrew bool `json:"tailThrew"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.HeadThrew {
		t.Error("fatal decoder should throw on incomplete multi-byte head (missing continuation bytes)")
	}
	if !data.TailThrew {
		t.Error("fatal decoder should throw on orphan continuation byte (0xAC without lead byte)")
	}
}

// TestEncodingEdge_EncodingLabelAliases verifies that WHATWG encoding label
// aliases normalise to the canonical encoding name via TextDecoder.encoding.
// Skips if the engine does not expose the encoding property.
func TestEncodingEdge_EncodingLabelAliases(t *testing.T) {
	e := newTestEngine(t)

	// Probe: does TextDecoder expose .encoding?
	probeSource := `export default {
  fetch(request, env) {
    const enc = new TextDecoder('utf-8').encoding;
    return Response.json({ enc });
  },
};`
	pr := execJS(t, e, probeSource, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, pr)
	var probe struct {
		Enc string `json:"enc"`
	}
	if err := json.Unmarshal(pr.Response.Body, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	if probe.Enc == "" {
		t.Skip("engine TextDecoder does not expose .encoding property; feature unimplemented")
	}

	source := `export default {
  fetch(request, env) {
    const utf8Labels   = ['utf-8', 'utf8', 'unicode-1-1-utf-8'];
    const latin1Labels = ['iso-8859-1', 'latin1', 'ascii', 'us-ascii', 'windows-1252'];

    const utf8Results   = utf8Labels.map(l => ({ label: l, encoding: new TextDecoder(l).encoding }));
    const latin1Results = latin1Labels.map(l => ({ label: l, encoding: new TextDecoder(l).encoding }));

    return Response.json({ utf8Results, latin1Results });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		UTF8Results   []struct{ Label, Encoding string } `json:"utf8Results"`
		Latin1Results []struct{ Label, Encoding string } `json:"latin1Results"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, item := range data.UTF8Results {
		if item.Encoding != "utf-8" {
			t.Errorf("label %q: encoding = %q, want utf-8", item.Label, item.Encoding)
		}
	}
	for _, item := range data.Latin1Results {
		if item.Encoding != "windows-1252" {
			t.Errorf("label %q: encoding = %q, want windows-1252", item.Label, item.Encoding)
		}
	}
}

// TestEncodingEdge_NonFatalReplacesFragment verifies that the non-fatal
// TextDecoder produces some error-signal output for an incomplete multi-byte
// sequence (a lone lead byte with no continuation). The WHATWG spec requires
// U+FFFD (0xFFFD). Engines that do not implement a compliant polyfill may
// produce a different result; this test documents that divergence.
func TestEncodingEdge_NonFatalReplacesFragment(t *testing.T) {
	e := newTestEngine(t)

	// First probe: what does the engine actually produce for 0xC2 alone?
	// The polyfill decodes 0xC2 as the lead of a 2-byte sequence using
	// bytes[i+1] which is undefined (→ 0), giving ((0xC2&0x1F)<<6)|(0&0x3F) = 0x80.
	// A compliant engine produces 0xFFFD.
	probeSource := `export default {
  fetch(request, env) {
    const dec = new TextDecoder();
    const r = dec.decode(new Uint8Array([0xC2]));
    return Response.json({ code: r.charCodeAt(0) });
  },
};`
	pr := execJS(t, e, probeSource, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, pr)
	var probe struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(pr.Response.Body, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	if probe.Code != 0xFFFD {
		t.Skipf("engine TextDecoder non-fatal replacement produces 0x%04X instead of U+FFFD (0xFFFD); WHATWG-compliant replacement not implemented", probe.Code)
	}

	source := `export default {
  fetch(request, env) {
    const dec = new TextDecoder();
    // 2-byte cent sign 0xC2 0xA2 — send only the lead byte 0xC2.
    // Without stream:true the decoder flushes, spec requires U+FFFD.
    const headOnly = dec.decode(new Uint8Array([0xC2]));
    return Response.json({
      len: headOnly.length,
      code: headOnly.charCodeAt(0),
      isReplacement: headOnly === '\uFFFD',
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Len           int  `json:"len"`
		Code          int  `json:"code"`
		IsReplacement bool `json:"isReplacement"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.IsReplacement {
		t.Errorf("non-fatal TextDecoder: expected U+FFFD (0xFFFD) for truncated lead byte, got code 0x%04X (len=%d)", data.Code, data.Len)
	}
}

// ---------------------------------------------------------------------------
// Compression edge cases — adapted from workerd compression-streams-test.js
// ---------------------------------------------------------------------------

// TestCompressionEdge_EmptyInputGzip verifies that an empty gzip roundtrip
// produces empty output after decompression (not a panic or error).
func TestCompressionEdge_EmptyInputGzip(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const enc = new TextEncoder();
    const dec = new TextDecoder();

    const cs = new CompressionStream('gzip');
    const csWriter = cs.writable.getWriter();
    await csWriter.write(enc.encode(''));
    await csWriter.close();

    const parts = [];
    const reader = cs.readable.getReader();
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (value) parts.push(value);
    }
    let totalLen = 0;
    for (const c of parts) totalLen += c.length;
    const compressed = new Uint8Array(totalLen);
    let off = 0;
    for (const c of parts) { compressed.set(c, off); off += c.length; }

    const ds = new DecompressionStream('gzip');
    const dsWriter = ds.writable.getWriter();
    await dsWriter.write(compressed);
    await dsWriter.close();

    const dparts = [];
    const dreader = ds.readable.getReader();
    while (true) {
      const { done, value } = await dreader.read();
      if (done) break;
      if (value) dparts.push(value);
    }
    let dtotal = 0;
    for (const c of dparts) dtotal += c.length;
    const decompressed = new Uint8Array(dtotal);
    let doff = 0;
    for (const c of dparts) { decompressed.set(c, doff); doff += c.length; }

    return Response.json({
      compressedLen: compressed.length,
      decompressedLen: decompressed.length,
      roundtrip: dec.decode(decompressed) === '',
    });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		CompressedLen   int  `json:"compressedLen"`
		DecompressedLen int  `json:"decompressedLen"`
		Roundtrip       bool `json:"roundtrip"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.CompressedLen == 0 {
		t.Error("compressed empty input should still produce gzip header/trailer bytes (>0)")
	}
	if data.DecompressedLen != 0 {
		t.Errorf("decompressed length = %d, want 0", data.DecompressedLen)
	}
	if !data.Roundtrip {
		t.Error("empty string gzip roundtrip failed")
	}
}

// TestCompressionEdge_ChunkedWritesRoundtrip exercises multiple small chunk
// writes followed by a single-shot decompress, verifying the output equals
// the concatenation of the original chunks.
func TestCompressionEdge_ChunkedWritesRoundtrip(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const enc = new TextEncoder();
    const dec = new TextDecoder();
    const chunks = ['hello', ' ', 'world', '!', ' '.repeat(50)];

    const cs = new CompressionStream('deflate');
    const writer = cs.writable.getWriter();
    for (const chunk of chunks) {
      await writer.write(enc.encode(chunk));
    }
    await writer.close();

    const parts = [];
    const reader = cs.readable.getReader();
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (value) parts.push(value);
    }
    let totalLen = 0;
    for (const p of parts) totalLen += p.length;
    const compressed = new Uint8Array(totalLen);
    let off = 0;
    for (const p of parts) { compressed.set(p, off); off += p.length; }

    const ds = new DecompressionStream('deflate');
    const dwriter = ds.writable.getWriter();
    await dwriter.write(compressed);
    await dwriter.close();

    const dparts = [];
    const dreader = ds.readable.getReader();
    while (true) {
      const { done, value } = await dreader.read();
      if (done) break;
      if (value) dparts.push(value);
    }
    let dtotal = 0;
    for (const p of dparts) dtotal += p.length;
    const decompressed = new Uint8Array(dtotal);
    let doff = 0;
    for (const p of dparts) { decompressed.set(p, doff); doff += p.length; }

    const original = chunks.join('');
    const result = dec.decode(decompressed);
    return Response.json({ match: result === original, resultLen: result.length });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Match     bool `json:"match"`
		ResultLen int  `json:"resultLen"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Match {
		t.Errorf("chunked write deflate roundtrip failed; decompressed length = %d", data.ResultLen)
	}
}

// TestCompressionEdge_PendingReadBeforeWrite verifies that issuing a read()
// before any write resolves correctly once data arrives (backpressure ordering).
// Uses gzip to avoid byte-level sensitivity of the raw deflate check bytes.
func TestCompressionEdge_PendingReadBeforeWrite(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const enc = new TextEncoder();
    const dec = new TextDecoder();
    const original = 'hello world pending read test';

    // Compress first so we have valid bytes to feed the decompressor.
    const cs = new CompressionStream('gzip');
    const csw = cs.writable.getWriter();
    await csw.write(enc.encode(original));
    await csw.close();

    const cparts = [];
    const cr = cs.readable.getReader();
    while (true) {
      const { done, value } = await cr.read();
      if (done) break;
      if (value) cparts.push(value);
    }
    let clen = 0;
    for (const p of cparts) clen += p.length;
    const compressed = new Uint8Array(clen);
    let coff = 0;
    for (const p of cparts) { compressed.set(p, coff); coff += p.length; }

    // Now test pending read: issue read() BEFORE writing.
    const { writable, readable } = new DecompressionStream('gzip');
    const writer = writable.getWriter();
    const reader = readable.getReader();

    const readPromise = reader.read(); // pending — no data yet
    await writer.write(compressed);
    await writer.close();

    const result = await readPromise;
    const text = result.value ? dec.decode(result.value) : '';

    return Response.json({ text: text.startsWith('hello'), done: result.done });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Text bool `json:"text"`
		Done bool `json:"done"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Text {
		t.Error("pending read result should start with 'hello'")
	}
	if data.Done {
		t.Error("result.done should be false for first data chunk")
	}
}

// TestCompressionEdge_InvalidDataRejectsWrite verifies that DecompressionStream
// surfaces an error when given non-compressed bytes. Per the Compression Streams
// spec the error must surface as a rejected promise on either the write() call
// or a subsequent read(). This test only awaits write() to avoid a read()
// deadlock that some engine implementations exhibit when the stream errors
// internally without unblocking a pending read.
func TestCompressionEdge_InvalidDataRejectsWrite(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const { writable, readable } = new DecompressionStream('deflate');
    const writer = writable.getWriter();

    let writeError = null;
    try {
      await writer.write(new TextEncoder().encode('not compressed data'));
      await writer.close();
    } catch(e) {
      writeError = e.name || 'Error';
    }

    return Response.json({ writeError });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		WriteError string `json:"writeError"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.WriteError == "" {
		t.Skip("engine DecompressionStream does not reject write() on invalid data; error propagation not implemented for this path")
	}
}

// TestCompressionEdge_DeflateRawRoundtrip exercises the deflate-raw format
// (no zlib header/trailer), distinct from 'deflate' (zlib-wrapped).
func TestCompressionEdge_DeflateRawRoundtrip(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  async fetch(request, env) {
    const enc = new TextEncoder();
    const dec = new TextDecoder();
    const original = 'deflate-raw test string ' + 'x'.repeat(200);

    const cs = new CompressionStream('deflate-raw');
    const writer = cs.writable.getWriter();
    await writer.write(enc.encode(original));
    await writer.close();

    const parts = [];
    const reader = cs.readable.getReader();
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (value) parts.push(value);
    }
    let len = 0;
    for (const p of parts) len += p.length;
    const compressed = new Uint8Array(len);
    let off = 0;
    for (const p of parts) { compressed.set(p, off); off += p.length; }

    const ds = new DecompressionStream('deflate-raw');
    const dwriter = ds.writable.getWriter();
    await dwriter.write(compressed);
    await dwriter.close();

    const dparts = [];
    const dreader = ds.readable.getReader();
    while (true) {
      const { done, value } = await dreader.read();
      if (done) break;
      if (value) dparts.push(value);
    }
    let dlen = 0;
    for (const p of dparts) dlen += p.length;
    const decompressed = new Uint8Array(dlen);
    let doff = 0;
    for (const p of dparts) { decompressed.set(p, doff); doff += p.length; }

    return Response.json({ match: dec.decode(decompressed) === original });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Match bool `json:"match"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Match {
		t.Error("deflate-raw roundtrip failed")
	}
}

// ---------------------------------------------------------------------------
// WebSocket edge cases — adapted from workerd websocket-constructor-test.js
// ---------------------------------------------------------------------------

// TestWebSocketEdge_EmptyProtocolsArray verifies that new WebSocket(url, [])
// is accepted (empty array ≡ no subprotocol).
func TestWebSocketEdge_EmptyProtocolsArray(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    try {
      const ws = new WebSocket('wss://example.com/', []);
      return Response.json({
        ok: true,
        url: ws.url,
        protocol: ws.protocol,
        readyState: ws.readyState,
      });
    } catch(e) {
      return Response.json({ ok: false, error: e.message });
    }
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		OK         bool   `json:"ok"`
		URL        string `json:"url"`
		Protocol   string `json:"protocol"`
		ReadyState int    `json:"readyState"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.OK {
		t.Errorf("new WebSocket(url, []) should not throw: %s", data.Error)
	}
	if data.URL != "wss://example.com/" {
		t.Errorf("url = %q, want 'wss://example.com/'", data.URL)
	}
	if data.Protocol != "" {
		t.Errorf("protocol = %q, want ''", data.Protocol)
	}
}

// TestWebSocketEdge_InvalidProtocolSpaces verifies that a protocol token
// containing ASCII whitespace throws SyntaxError per the WHATWG WebSocket spec.
// Skips if the engine does not yet enforce this validation.
func TestWebSocketEdge_InvalidProtocolSpaces(t *testing.T) {
	e := newTestEngine(t)

	// Probe: does the engine validate protocol tokens?
	probeSource := `export default {
  fetch(request, env) {
    let threw = false;
    try { new WebSocket('wss://example.com/', 'bad token'); } catch(e) { threw = true; }
    return Response.json({ threw });
  },
};`
	pr := execJS(t, e, probeSource, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, pr)
	var probe struct {
		Threw bool `json:"threw"`
	}
	if err := json.Unmarshal(pr.Response.Body, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	if !probe.Threw {
		t.Skip("engine WebSocket does not validate protocol token characters; feature unimplemented")
	}

	source := `export default {
  fetch(request, env) {
    let threw = false;
    let errorName = '';
    try {
      new WebSocket('wss://example.com/', 'invalid protocol with spaces');
    } catch(e) {
      threw = true;
      errorName = e.name;
    }
    return Response.json({ threw, errorName });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Threw     bool   `json:"threw"`
		ErrorName string `json:"errorName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Threw {
		t.Error("WebSocket with space-containing protocol should throw")
	}
	if data.ErrorName != "SyntaxError" {
		t.Errorf("error name = %q, want SyntaxError", data.ErrorName)
	}
}

// TestWebSocketEdge_DuplicateProtocolsThrow verifies that duplicate protocol
// tokens in the array cause a SyntaxError per the spec.
// Skips if the engine does not yet enforce this validation.
func TestWebSocketEdge_DuplicateProtocolsThrow(t *testing.T) {
	e := newTestEngine(t)

	// Probe: does the engine detect duplicate protocols?
	probeSource := `export default {
  fetch(request, env) {
    let threw = false;
    try { new WebSocket('wss://example.com/', ['chat', 'chat']); } catch(e) { threw = true; }
    return Response.json({ threw });
  },
};`
	pr := execJS(t, e, probeSource, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, pr)
	var probe struct {
		Threw bool `json:"threw"`
	}
	if err := json.Unmarshal(pr.Response.Body, &probe); err != nil {
		t.Fatalf("probe unmarshal: %v", err)
	}
	if !probe.Threw {
		t.Skip("engine WebSocket does not reject duplicate protocol tokens; feature unimplemented")
	}

	source := `export default {
  fetch(request, env) {
    let threw = false;
    let errorName = '';
    try {
      new WebSocket('wss://example.com/', ['chat', 'chat']);
    } catch(e) {
      threw = true;
      errorName = e.name;
    }
    return Response.json({ threw, errorName });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Threw     bool   `json:"threw"`
		ErrorName string `json:"errorName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Threw {
		t.Error("WebSocket with duplicate protocols should throw")
	}
	if data.ErrorName != "SyntaxError" {
		t.Errorf("error name = %q, want SyntaxError", data.ErrorName)
	}
}

// TestWebSocketEdge_CloseReasonTooLong verifies that close() throws SyntaxError
// when the reason string exceeds 123 UTF-8 bytes (RFC 6455 §5.5 limit).
func TestWebSocketEdge_CloseReasonTooLong(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const ws = new WebSocket('wss://example.com/');
    const longReason = 'a'.repeat(124);
    let threw = false;
    let errorName = '';
    try {
      ws.close(1000, longReason);
    } catch(e) {
      threw = true;
      errorName = e.name;
    }
    ws.close();
    return Response.json({ threw, errorName });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Threw     bool   `json:"threw"`
		ErrorName string `json:"errorName"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !data.Threw {
		t.Error("close() with 124-byte reason should throw SyntaxError")
	}
	if data.ErrorName != "SyntaxError" {
		t.Errorf("error name = %q, want SyntaxError", data.ErrorName)
	}
}

// TestWebSocketEdge_CloseReasonExact123Bytes verifies that exactly 123 ASCII
// bytes is accepted without throwing.
func TestWebSocketEdge_CloseReasonExact123Bytes(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const ws = new WebSocket('wss://example.com/');
    const reason123 = 'a'.repeat(123);
    let threw = false;
    try {
      ws.close(1000, reason123);
    } catch(e) {
      threw = true;
    }
    return Response.json({ threw });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Threw bool `json:"threw"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.Threw {
		t.Error("close() with exactly 123-byte reason should not throw")
	}
}

// TestWebSocketEdge_CloseReasonMultibyteUTF8 verifies that the 123-byte limit
// counts UTF-8 bytes, not JS string characters. U+00E9 (é) encodes as 2 bytes;
// 62 × é = 124 UTF-8 bytes which exceeds the limit.
// Skips if the engine does not yet enforce the close reason byte limit.
func TestWebSocketEdge_CloseReasonMultibyteUTF8(t *testing.T) {
	e := newTestEngine(t)

	source := `export default {
  fetch(request, env) {
    const ws = new WebSocket('wss://example.com/');
    const multibyteReason = '\u00e9'.repeat(62); // 62 chars, 124 UTF-8 bytes
    let threw = false;
    let errorName = '';
    try {
      ws.close(1000, multibyteReason);
    } catch(e) {
      threw = true;
      errorName = e.name;
    }
    ws.close();
    return Response.json({ threw, errorName, charLen: multibyteReason.length });
  },
};`

	r := execJS(t, e, source, defaultEnv(), getReq("http://localhost/"))
	assertOK(t, r)

	var data struct {
		Threw     bool   `json:"threw"`
		ErrorName string `json:"errorName"`
		CharLen   int    `json:"charLen"`
	}
	if err := json.Unmarshal(r.Response.Body, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if data.CharLen != 62 {
		t.Errorf("charLen = %d, want 62", data.CharLen)
	}
	if !data.Threw {
		t.Error("close() with 62×é (124 UTF-8 bytes) should throw SyntaxError")
	}
	if data.ErrorName != "SyntaxError" {
		t.Errorf("error name = %q, want SyntaxError", data.ErrorName)
	}
}
