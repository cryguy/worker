package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/cryguy/worker/v2/internal/core"
	"github.com/cryguy/worker/v2/internal/eventloop"
)

const (
	sentinelUndef   = "\x01"
	sentinelPromise = "\x02"
)

func main() {
	cfg := core.EngineConfig{
		MemoryLimitMB:    128,
		MaxFetchRequests: 100,
		ExecutionTimeout: 30000,
		FetchTimeoutSec:  30,
	}

	rt, el, cleanup, err := newRuntime(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create runtime: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()

	// Persistent request state for APIs that need it (crypto, fetch, etc.)
	env := &core.Env{Vars: map[string]string{}}
	reqID := core.NewRequestState(cfg.MaxFetchRequests, env)
	defer core.ClearRequestState(reqID)
	_ = rt.SetGlobal("__requestID", fmt.Sprintf("%d", reqID))

	// Register a fresh Go function for shell output (re-registering __console
	// doesn't reliably replace the original, so use a new name).
	if err := rt.RegisterFunc("__shell_print", func(level, msg string) {
		switch level {
		case "error":
			fmt.Fprintln(os.Stderr, msg)
		case "warn":
			fmt.Fprintln(os.Stderr, msg)
		default:
			fmt.Println(msg)
		}
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to register shell print: %v\n", err)
		os.Exit(1)
	}

	// Replace console methods to print to terminal instead of buffering
	_ = rt.Eval(`(function() {
		var levels = ['log', 'info', 'warn', 'error', 'debug'];
		for (var i = 0; i < levels.length; i++) {
			(function(lvl) {
				console[lvl] = function() {
					var parts = [];
					for (var j = 0; j < arguments.length; j++) {
						var arg = arguments[j];
						if (typeof arg === 'object' && arg !== null) {
							try { parts.push(JSON.stringify(arg)); } catch(e) { parts.push(String(arg)); }
						} else {
							parts.push(String(arg));
						}
					}
					__shell_print(lvl, parts.join(' '));
				};
			})(levels[i]);
		}
	})()`)

	// Result formatter for expression evaluation
	_ = rt.Eval(`globalThis.__fmt = function(v) {
		if (v === undefined) return '\x01';
		if (v === null) return 'null';
		if (typeof v === 'string') return "'" + v.replace(/\\/g, '\\\\').replace(/'/g, "\\'") + "'";
		if (typeof v === 'function') return v.toString();
		if (typeof v === 'symbol') return v.toString();
		if (typeof v === 'object') {
			if (typeof v.then === 'function') {
				v.then(function(r) {
					if (r !== undefined) {
						var s = (typeof r === 'object' && r !== null) ? JSON.stringify(r, null, 2) : String(r);
						console.log(s);
					}
				}).catch(function(e) {
					console.error('Uncaught (in promise): ' + e);
				});
				return '\x02';
			}
			try { return JSON.stringify(v, null, 2); } catch(e) { return String(v); }
		}
		return String(v);
	}`)

	// Handle Ctrl+C
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		fmt.Println()
		os.Exit(0)
	}()

	fmt.Printf("Worker JS shell (%s)\n", engineName)
	fmt.Println("Type .exit to quit, .help for commands")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for {
		fmt.Print("> ")

		input := readInput(scanner)
		if input == "" {
			continue
		}

		trimmed := strings.TrimSpace(input)
		switch trimmed {
		case ".exit", ".quit":
			return
		case ".help":
			printHelp()
			continue
		}

		evalInput(rt, el, input, trimmed)
	}
}

func evalInput(rt core.JSRuntime, el *eventloop.EventLoop, input, trimmed string) {
	// let/const/class need script-level eval to persist bindings
	if isLexicalDecl(trimmed) {
		if err := rt.Eval(input); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
		}
	} else {
		_ = rt.SetGlobal("__code", input)
		result, err := rt.EvalString("__fmt((1,eval)(__code))")
		if err != nil {
			// Fallback: try as statement (for, while, if, try, etc.)
			if err2 := rt.Eval(input); err2 != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err2)
			}
		} else {
			switch result {
			case sentinelUndef:
				// silent
			case sentinelPromise:
				fmt.Println("Promise { <pending> }")
			default:
				fmt.Println(result)
			}
		}
	}

	rt.RunMicrotasks()

	if el.HasPending() {
		el.Drain(rt, time.Now().Add(5*time.Second))
	}
}

func isLexicalDecl(s string) bool {
	return strings.HasPrefix(s, "let ") || strings.HasPrefix(s, "let\t") ||
		strings.HasPrefix(s, "const ") || strings.HasPrefix(s, "const\t") ||
		strings.HasPrefix(s, "class ") || strings.HasPrefix(s, "class\t") || strings.HasPrefix(s, "class{")
}

func readInput(scanner *bufio.Scanner) string {
	if !scanner.Scan() {
		fmt.Println()
		os.Exit(0)
	}

	line := scanner.Text()
	lines := []string{line}
	depth := bracketDepth(line)

	for depth > 0 {
		fmt.Print("... ")
		if !scanner.Scan() {
			break
		}
		line = scanner.Text()
		lines = append(lines, line)
		depth += bracketDepth(line)
	}

	return strings.Join(lines, "\n")
}

func bracketDepth(line string) int {
	depth := 0
	inStr := false
	strCh := byte(0)
	esc := false

	for i := 0; i < len(line); i++ {
		c := line[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' && inStr {
			esc = true
			continue
		}
		if inStr {
			if c == strCh {
				inStr = false
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			inStr = true
			strCh = c
		case '{', '(', '[':
			depth++
		case '}', ')', ']':
			depth--
		case '/':
			if i+1 < len(line) && line[i+1] == '/' {
				return depth
			}
		}
	}

	return depth
}

func printHelp() {
	fmt.Println("Commands:")
	fmt.Println("  .exit    Exit the shell")
	fmt.Println("  .help    Show this help")
	fmt.Println()
	fmt.Println("Available APIs:")
	fmt.Println("  fetch, URL, Request, Response, Headers")
	fmt.Println("  crypto.subtle, crypto.getRandomValues")
	fmt.Println("  TextEncoder, TextDecoder, atob, btoa")
	fmt.Println("  setTimeout, setInterval")
	fmt.Println("  console.log/warn/error/table/time/count")
	fmt.Println("  ReadableStream, WritableStream, TransformStream")
	fmt.Println("  CompressionStream, DecompressionStream")
	fmt.Println("  WebSocket, EventSource, HTMLRewriter")
	fmt.Println("  structuredClone, queueMicrotask")
	fmt.Println()
	fmt.Println("Notes:")
	fmt.Println("  let/const/class declarations persist across lines")
	fmt.Println("  Promises auto-resolve and print results")
	fmt.Println("  Multi-line input detected via open brackets")
}
