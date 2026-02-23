package worker

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"

	gohtml "golang.org/x/net/html"
	"modernc.org/quickjs"
)

// maxHTMLRewriterHandlers caps the number of selector handlers to prevent CPU DoS.
const maxHTMLRewriterHandlers = 64

// htmlRewriterJS defines the HTMLRewriter class available to workers.
// Follows the Cloudflare Workers HTMLRewriter API:
//   - new HTMLRewriter().on(selector, { element, text, comments }).onDocument({ end }).transform(response)
const htmlRewriterJS = `
(function() {

class HTMLRewriter {
	constructor() {
		this._handlers = [];
		this._docHandlers = {};
	}

	on(selector, handlers) {
		this._handlers.push({ selector: selector, handlers: handlers });
		return this;
	}

	onDocument(handlers) {
		this._docHandlers = handlers;
		return this;
	}

	transform(response) {
		if (!response) return response;

		// Get body as string.
		var body;
		if (response._body === null || response._body === undefined) {
			return response;
		}
		if (typeof response._body === 'string') {
			body = response._body;
		} else {
			body = String(response._body);
		}

		// Store handlers for Go callback access.
		globalThis.__htmlrw_handlers = this._handlers;
		globalThis.__htmlrw_doc_handlers = this._docHandlers;

		// Call Go-backed transformation.
		var transformed = __htmlRewrite(body);

		delete globalThis.__htmlrw_handlers;
		delete globalThis.__htmlrw_doc_handlers;

		return new Response(transformed, {
			status: response.status,
			statusText: response.statusText,
			headers: new Headers(response.headers),
		});
	}
}

globalThis.HTMLRewriter = HTMLRewriter;

})();
`

// setupHTMLRewriter registers the HTMLRewriter JS class and the Go-backed
// __htmlRewrite function that performs streaming HTML transformation.
func setupHTMLRewriter(vm *quickjs.VM, _ *eventLoop) error {
	// Register __htmlRewrite(htmlString) — transforms HTML using registered handlers.
	err := registerGoFunc(vm, "__htmlRewrite", func(htmlStr string) string {
		result, err := rewriteHTML(vm, htmlStr)
		if err != nil {
			// Return original HTML on error.
			return htmlStr
		}
		return result
	}, false)
	if err != nil {
		return fmt.Errorf("registering __htmlRewrite: %w", err)
	}

	// Evaluate the JS class.
	if err := evalDiscard(vm, htmlRewriterJS); err != nil {
		return fmt.Errorf("evaluating htmlrewriter.js: %w", err)
	}
	return nil
}

// handlerSpec describes a registered selector handler from JS.
type handlerSpec struct {
	selector   *compoundSelector
	handlerIdx int
}

// matchedElement tracks state for elements matched by selectors during rewriting.
type matchedElement struct {
	handlerIdx    int
	depth         int
	skipContent   bool   // setInnerContent or remove was called
	removed       bool   // element.remove() was called (skip end tag too)
	newTagName    string // tagName changed by handler (for end tag rewriting)
	innerContent  string // replacement content for setInnerContent
	appendContent string // element.append() — emitted before end tag
	afterContent  string // element.after() — emitted after end tag
}

// rewriteHTML tokenizes HTML, matches selectors, calls JS handlers, and
// applies mutations to produce transformed output.
func rewriteHTML(vm *quickjs.VM, htmlStr string) (string, error) {
	// Read handler registrations from JS globals.
	handlersCount, err := evalInt(vm, `
		globalThis.__htmlrw_handlers ? globalThis.__htmlrw_handlers.length : 0
	`)
	if err != nil {
		return htmlStr, err
	}
	if handlersCount > maxHTMLRewriterHandlers {
		handlersCount = maxHTMLRewriterHandlers
	}

	// Parse selectors for each handler.
	var specs []handlerSpec
	for i := 0; i < handlersCount; i++ {
		selStr, err := evalString(vm, fmt.Sprintf(`globalThis.__htmlrw_handlers[%d].selector`, i))
		if err != nil {
			continue
		}
		sel := parseCompoundSelector(selStr)
		specs = append(specs, handlerSpec{selector: sel, handlerIdx: i})
	}

	// Tokenize and transform.
	tokenizer := gohtml.NewTokenizer(strings.NewReader(htmlStr))
	var out strings.Builder

	var matchStack []*matchedElement
	depth := 0

	// DOM context tracking for combinator matching.
	// elementStack tracks open elements (ancestors of the current position).
	var elementStack []elementInfo
	// siblingMap tracks previous siblings at each depth level.
	// Key is depth, value is ordered list of element infos at that depth.
	siblingMap := make(map[int][]elementInfo)

	// needsContext is true if any spec uses combinators.
	needsContext := false
	for _, spec := range specs {
		if !spec.selector.isSimple() {
			needsContext = true
			break
		}
	}

	// selectorMatches checks whether a spec matches the given element,
	// using context-aware matching for compound selectors.
	selectorMatches := func(spec handlerSpec, tagName string, attrs map[string]string) bool {
		if spec.selector.isSimple() {
			return spec.selector.subject().matches(tagName, attrs)
		}
		var siblings []elementInfo
		if needsContext {
			siblings = siblingMap[depth]
		}
		return spec.selector.matchesWithContext(tagName, attrs, elementStack, siblings)
	}

	for {
		tt := tokenizer.Next()
		if tt == gohtml.ErrorToken {
			break
		}

		token := tokenizer.Token()

		switch tt {
		case gohtml.StartTagToken:
			isVoid := voidElement(token.Data)
			depth++

			// If inside a removed/replaced element, skip everything.
			if shouldSkipContent(matchStack, depth) {
				if isVoid {
					depth-- // void elements have no end tag
				}
				continue
			}

			attrs := htmlAttrsToMap(token.Attr)

			// Check selectors.
			matched := false
			for _, spec := range specs {
				if !selectorMatches(spec, token.Data, attrs) {
					continue
				}
				mutations := callElementHandler(vm, spec.handlerIdx, token.Data, attrs)

				if mutations == nil {
					// No element handler, but selector matched.
					// Track on matchStack so text/comment handlers can fire.
					matched = true
					out.WriteString(token.String())
					if !isVoid {
						matchStack = append(matchStack, &matchedElement{
							handlerIdx: spec.handlerIdx,
							depth:      depth,
						})
					}
					break
				}
				matched = true

				// Before content.
				out.WriteString(mutations.Before)

				if mutations.Removed {
					if !isVoid {
						matchStack = append(matchStack, &matchedElement{
							handlerIdx:   spec.handlerIdx,
							depth:        depth,
							skipContent:  true,
							removed:      true,
							afterContent: mutations.After,
						})
					} else {
						// Void elements have no children or end tag.
						out.WriteString(mutations.After)
					}
					break
				}

				// Rebuild start tag with modified attributes.
				tagName := token.Data
				if mutations.NewTagName != "" {
					tagName = mutations.NewTagName
				}
				out.WriteByte('<')
				out.WriteString(tagName)
				for k, v := range mutations.Attrs {
					out.WriteByte(' ')
					out.WriteString(k)
					out.WriteString(`="`)
					out.WriteString(html.EscapeString(v))
					out.WriteByte('"')
				}
				if isVoid {
					out.WriteString(" />")
					out.WriteString(mutations.After)
				} else {
					out.WriteByte('>')
					out.WriteString(mutations.Prepend)

					me := &matchedElement{
						handlerIdx:    spec.handlerIdx,
						depth:         depth,
						newTagName:    mutations.NewTagName,
						appendContent: mutations.Append,
						afterContent:  mutations.After,
					}
					if mutations.InnerContent != "" {
						me.skipContent = true
						me.innerContent = mutations.InnerContent
					}
					matchStack = append(matchStack, me)
				}
				break
			}

			if !matched {
				out.WriteString(token.String())
			}

			// Update DOM context: record this element as a sibling at current depth,
			// then push onto element stack if not void.
			if needsContext {
				info := elementInfo{tagName: token.Data, attrs: attrs, depth: depth}
				siblingMap[depth] = append(siblingMap[depth], info)
				if !isVoid {
					elementStack = append(elementStack, info)
					// Clear sibling tracking for the new child depth.
					delete(siblingMap, depth+1)
				}
			}

			// Void elements have no end tag — undo depth increment.
			if isVoid {
				depth--
			}

		case gohtml.EndTagToken:
			// Process matched element closure.
			var skipEndTag bool
			var afterContent string
			var rewrittenTag string
			for i := len(matchStack) - 1; i >= 0; i-- {
				me := matchStack[i]
				if me.depth == depth {
					if me.innerContent != "" {
						out.WriteString(me.innerContent)
					}
					out.WriteString(me.appendContent)
					skipEndTag = me.removed
					afterContent = me.afterContent
					rewrittenTag = me.newTagName
					matchStack = append(matchStack[:i], matchStack[i+1:]...)
					break
				}
			}

			// Pop element stack for context tracking.
			if needsContext && len(elementStack) > 0 && elementStack[len(elementStack)-1].depth == depth {
				elementStack = elementStack[:len(elementStack)-1]
				// Clear sibling tracking for the depth we're leaving.
				delete(siblingMap, depth+1)
			}

			depth--

			// Skip if inside a removed/replaced parent or this element was removed.
			if skipEndTag || shouldSkipContent(matchStack, depth+1) {
				out.WriteString(afterContent)
				continue
			}

			// Use rewritten tag name if the handler changed it.
			if rewrittenTag != "" && rewrittenTag != token.Data {
				out.WriteString("</" + rewrittenTag + ">")
			} else {
				out.WriteString(token.String())
			}
			out.WriteString(afterContent)

		case gohtml.TextToken:
			if shouldSkipContent(matchStack, depth) {
				continue
			}

			textContent := token.Data
			handled := false

			// Call text handlers for matched parent elements.
			for _, me := range matchStack {
				if !me.skipContent && depth >= me.depth {
					mutations := callTextHandler(vm, me.handlerIdx, textContent, false)
					if mutations != nil {
						handled = true
						out.WriteString(mutations.Before)
						if mutations.Removed {
							// skip the text
						} else if mutations.Replacement != "" {
							out.WriteString(mutations.Replacement)
						} else {
							out.WriteString(textContent)
						}
						out.WriteString(mutations.After)
						break
					}
				}
			}

			// Document-level text handler.
			docMut := callDocTextHandler(vm, textContent)
			if docMut != nil && !handled {
				out.WriteString(docMut.Before)
				if docMut.Removed {
					// skip
				} else if docMut.Replacement != "" {
					out.WriteString(docMut.Replacement)
				} else {
					out.WriteString(textContent)
				}
				out.WriteString(docMut.After)
			} else if !handled {
				out.WriteString(textContent)
			}

		case gohtml.CommentToken:
			if shouldSkipContent(matchStack, depth) {
				continue
			}

			handled := false
			for _, me := range matchStack {
				if !me.skipContent && depth >= me.depth {
					mutations := callCommentHandler(vm, me.handlerIdx, token.Data)
					if mutations != nil {
						handled = true
						out.WriteString(mutations.Before)
						if mutations.Removed {
							// skip
						} else if mutations.Replacement != "" {
							out.WriteString(mutations.Replacement)
						} else {
							out.WriteString("<!--")
							out.WriteString(token.Data)
							out.WriteString("-->")
						}
						out.WriteString(mutations.After)
						break
					}
				}
			}
			if !handled {
				out.WriteString("<!--")
				out.WriteString(token.Data)
				out.WriteString("-->")
			}

		case gohtml.DoctypeToken:
			out.WriteString(token.String())

		case gohtml.SelfClosingTagToken:
			if shouldSkipContent(matchStack, depth) {
				continue
			}

			attrs := htmlAttrsToMap(token.Attr)
			handled := false

			for _, spec := range specs {
				if !selectorMatches(spec, token.Data, attrs) {
					continue
				}
				mutations := callElementHandler(vm, spec.handlerIdx, token.Data, attrs)
				if mutations == nil {
					continue
				}
				handled = true
				out.WriteString(mutations.Before)
				if !mutations.Removed {
					tagName := token.Data
					if mutations.NewTagName != "" {
						tagName = mutations.NewTagName
					}
					out.WriteByte('<')
					out.WriteString(tagName)
					for k, v := range mutations.Attrs {
						out.WriteByte(' ')
						out.WriteString(k)
						out.WriteString(`="`)
						out.WriteString(html.EscapeString(v))
						out.WriteByte('"')
					}
					out.WriteString(" />")
				}
				out.WriteString(mutations.After)
				break
			}

			// Track self-closing elements as siblings for context.
			if needsContext {
				info := elementInfo{tagName: token.Data, attrs: attrs, depth: depth + 1}
				siblingMap[depth+1] = append(siblingMap[depth+1], info)
			}

			if !handled {
				out.WriteString(token.String())
			}
		default:
			panic("unhandled default case")
		}
	}

	// Call document end handler.
	endMutations := callDocEndHandler(vm)
	if endMutations != "" {
		out.WriteString(endMutations)
	}

	return out.String(), nil
}

// shouldSkipContent returns true if the current depth is inside a matched
// element that has had its content replaced (setInnerContent) or removed.
func shouldSkipContent(stack []*matchedElement, depth int) bool {
	for _, me := range stack {
		if me.skipContent && depth >= me.depth {
			return true
		}
	}
	return false
}

// elementMutations captures the changes an element handler requested.
type elementMutations struct {
	Before       string
	After        string
	Prepend      string
	Append       string
	InnerContent string
	Removed      bool
	NewTagName   string
	Attrs        map[string]string
}

// textMutations captures changes a text handler requested.
type textMutations struct {
	Before      string
	After       string
	Replacement string
	Removed     bool
}

// callElementHandler invokes the JS element handler and returns mutations.
func callElementHandler(vm *quickjs.VM, handlerIdx int, tagName string, attrs map[string]string) *elementMutations {
	attrsJSON, _ := json.Marshal(attrs)

	// Set globals
	if err := setGlobal(vm, "__el_tag", tagName); err != nil {
		return nil
	}
	if err := setGlobal(vm, "__el_attrs", string(attrsJSON)); err != nil {
		return nil
	}
	if err := evalDiscard(vm, fmt.Sprintf("globalThis.__el_handler_idx = %d;", handlerIdx)); err != nil {
		return nil
	}

	result, err := evalString(vm, `
		(function() {
			var tag = globalThis.__el_tag;
			var attrsObj = JSON.parse(globalThis.__el_attrs);
			var idx = globalThis.__el_handler_idx;
			delete globalThis.__el_tag;
			delete globalThis.__el_attrs;
			delete globalThis.__el_handler_idx;

			var handler = globalThis.__htmlrw_handlers[idx].handlers;
			if (!handler || typeof handler.element !== 'function') return 'null';

			var mutations = [];
			var newAttrs = Object.assign({}, attrsObj);
			var newTag = tag;
			var el = {
				tagName: tag,
				get attributes() { return Object.entries(newAttrs); },
				getAttribute: function(n) { return newAttrs[n] !== undefined ? newAttrs[n] : null; },
				setAttribute: function(n, v) { newAttrs[n] = String(v); },
				removeAttribute: function(n) { delete newAttrs[n]; },
				hasAttribute: function(n) { return n in newAttrs; },
				before: function(c, o) { mutations.push({t:'before',c:c,h:o&&o.html}); },
				after: function(c, o) { mutations.push({t:'after',c:c,h:o&&o.html}); },
				prepend: function(c, o) { mutations.push({t:'prepend',c:c,h:o&&o.html}); },
				append: function(c, o) { mutations.push({t:'append',c:c,h:o&&o.html}); },
				setInnerContent: function(c, o) { mutations.push({t:'inner',c:c,h:o&&o.html}); },
				remove: function() { mutations.push({t:'remove'}); },
				get removed() { return mutations.some(function(m){return m.t==='remove';}); },
				set tagName(v) { newTag = v; },
			};
			Object.defineProperty(el, 'tagName', {
				get: function() { return newTag; },
				set: function(v) { newTag = v; },
			});

			handler.element(el);

			return JSON.stringify({
				mutations: mutations,
				attrs: newAttrs,
				tagName: newTag,
			});
		})()
	`)
	if err != nil || result == "null" {
		return nil
	}

	var parsed struct {
		Mutations []struct {
			T string `json:"t"`
			C string `json:"c"`
			H bool   `json:"h"`
		} `json:"mutations"`
		Attrs   map[string]string `json:"attrs"`
		TagName string            `json:"tagName"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return nil
	}

	m := &elementMutations{
		Attrs:      parsed.Attrs,
		NewTagName: parsed.TagName,
	}

	for _, mut := range parsed.Mutations {
		content := mut.C
		if !mut.H {
			content = html.EscapeString(content)
		}
		switch mut.T {
		case "before":
			m.Before += content
		case "after":
			m.After += content
		case "prepend":
			m.Prepend += content
		case "append":
			m.Append += content
		case "inner":
			m.InnerContent = content
		case "remove":
			m.Removed = true
		}
	}

	return m
}

// callTextHandler invokes the JS text handler and returns mutations.
func callTextHandler(vm *quickjs.VM, handlerIdx int, text string, last bool) *textMutations {
	if err := setGlobal(vm, "__text_content", text); err != nil {
		return nil
	}
	if err := evalDiscard(vm, fmt.Sprintf("globalThis.__text_last = %t;", last)); err != nil {
		return nil
	}
	if err := evalDiscard(vm, fmt.Sprintf("globalThis.__text_handler_idx = %d;", handlerIdx)); err != nil {
		return nil
	}

	result, err := evalString(vm, `
		(function() {
			var content = globalThis.__text_content;
			var isLast = globalThis.__text_last;
			var idx = globalThis.__text_handler_idx;
			delete globalThis.__text_content;
			delete globalThis.__text_last;
			delete globalThis.__text_handler_idx;

			var handler = globalThis.__htmlrw_handlers[idx].handlers;
			if (!handler || typeof handler.text !== 'function') return 'null';

			var mutations = [];
			var t = {
				text: content,
				lastInTextNode: isLast,
				before: function(c, o) { mutations.push({t:'before',c:c,h:o&&o.html}); },
				after: function(c, o) { mutations.push({t:'after',c:c,h:o&&o.html}); },
				replace: function(c, o) { mutations.push({t:'replace',c:c,h:o&&o.html}); },
				remove: function() { mutations.push({t:'remove'}); },
			};

			handler.text(t);

			return JSON.stringify(mutations);
		})()
	`)
	if err != nil || result == "null" {
		return nil
	}

	var muts []struct {
		T string `json:"t"`
		C string `json:"c"`
		H bool   `json:"h"`
	}
	if err := json.Unmarshal([]byte(result), &muts); err != nil {
		return nil
	}

	if len(muts) == 0 {
		return nil
	}

	m := &textMutations{}
	for _, mut := range muts {
		content := mut.C
		if !mut.H {
			content = html.EscapeString(content)
		}
		switch mut.T {
		case "before":
			m.Before += content
		case "after":
			m.After += content
		case "replace":
			m.Replacement = content
		case "remove":
			m.Removed = true
		}
	}
	return m
}

// callCommentHandler invokes the JS comment handler and returns mutations.
func callCommentHandler(vm *quickjs.VM, handlerIdx int, comment string) *textMutations {
	if err := setGlobal(vm, "__comment_content", comment); err != nil {
		return nil
	}
	if err := evalDiscard(vm, fmt.Sprintf("globalThis.__comment_handler_idx = %d;", handlerIdx)); err != nil {
		return nil
	}

	result, err := evalString(vm, `
		(function() {
			var content = globalThis.__comment_content;
			var idx = globalThis.__comment_handler_idx;
			delete globalThis.__comment_content;
			delete globalThis.__comment_handler_idx;

			var handler = globalThis.__htmlrw_handlers[idx].handlers;
			if (!handler || typeof handler.comments !== 'function') return 'null';

			var mutations = [];
			var c = {
				text: content,
				before: function(ct, o) { mutations.push({t:'before',c:ct,h:o&&o.html}); },
				after: function(ct, o) { mutations.push({t:'after',c:ct,h:o&&o.html}); },
				replace: function(ct, o) { mutations.push({t:'replace',c:ct,h:o&&o.html}); },
				remove: function() { mutations.push({t:'remove'}); },
			};

			handler.comments(c);

			return JSON.stringify(mutations);
		})()
	`)
	if err != nil || result == "null" {
		return nil
	}

	var muts []struct {
		T string `json:"t"`
		C string `json:"c"`
		H bool   `json:"h"`
	}
	if err := json.Unmarshal([]byte(result), &muts); err != nil {
		return nil
	}
	if len(muts) == 0 {
		return nil
	}

	m := &textMutations{}
	for _, mut := range muts {
		content := mut.C
		if !mut.H {
			content = html.EscapeString(content)
		}
		switch mut.T {
		case "before":
			m.Before += content
		case "after":
			m.After += content
		case "replace":
			m.Replacement = content
		case "remove":
			m.Removed = true
		}
	}
	return m
}

// callDocTextHandler calls the document-level text handler if registered.
func callDocTextHandler(vm *quickjs.VM, text string) *textMutations {
	if err := setGlobal(vm, "__doc_text_content", text); err != nil {
		return nil
	}

	result, err := evalString(vm, `
		(function() {
			var content = globalThis.__doc_text_content;
			delete globalThis.__doc_text_content;

			var handler = globalThis.__htmlrw_doc_handlers;
			if (!handler || typeof handler.text !== 'function') return 'null';

			var mutations = [];
			var t = {
				text: content,
				lastInTextNode: true,
				before: function(c, o) { mutations.push({t:'before',c:c,h:o&&o.html}); },
				after: function(c, o) { mutations.push({t:'after',c:c,h:o&&o.html}); },
				replace: function(c, o) { mutations.push({t:'replace',c:c,h:o&&o.html}); },
				remove: function() { mutations.push({t:'remove'}); },
			};

			handler.text(t);

			return JSON.stringify(mutations);
		})()
	`)
	if err != nil || result == "null" {
		return nil
	}

	var muts []struct {
		T string `json:"t"`
		C string `json:"c"`
		H bool   `json:"h"`
	}
	if err := json.Unmarshal([]byte(result), &muts); err != nil {
		return nil
	}
	if len(muts) == 0 {
		return nil
	}

	m := &textMutations{}
	for _, mut := range muts {
		content := mut.C
		if !mut.H {
			content = html.EscapeString(content)
		}
		switch mut.T {
		case "before":
			m.Before += content
		case "after":
			m.After += content
		case "replace":
			m.Replacement = content
		case "remove":
			m.Removed = true
		}
	}
	return m
}

// callDocEndHandler calls the document end handler and returns any appended content.
func callDocEndHandler(vm *quickjs.VM) string {
	result, err := evalString(vm, `
		(function() {
			var handler = globalThis.__htmlrw_doc_handlers;
			if (!handler || typeof handler.end !== 'function') return 'null';

			var appended = [];
			var end = {
				append: function(c, o) {
					appended.push({ c: c, h: o && o.html });
				},
			};

			handler.end(end);

			if (appended.length === 0) return 'null';
			return JSON.stringify(appended);
		})()
	`)
	if err != nil || result == "null" {
		return ""
	}

	var items []struct {
		C string `json:"c"`
		H bool   `json:"h"`
	}
	if err := json.Unmarshal([]byte(result), &items); err != nil {
		return ""
	}

	var out strings.Builder
	for _, item := range items {
		if item.H {
			out.WriteString(item.C)
		} else {
			out.WriteString(html.EscapeString(item.C))
		}
	}
	return out.String()
}

// htmlAttrsToMap converts html.Attribute slice to a string map.
func htmlAttrsToMap(attrs []gohtml.Attribute) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Val
	}
	return m
}

// voidElement returns true for HTML void elements that have no end tag.
func voidElement(tag string) bool {
	switch strings.ToLower(tag) {
	case "area", "base", "br", "col", "embed", "hr", "img", "input",
		"link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}
