package webapi

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"

	gohtml "golang.org/x/net/html"
	"github.com/cryguy/worker/internal/core"
	"github.com/cryguy/worker/internal/eventloop"
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

// SetupHTMLRewriter registers the HTMLRewriter JS class and the Go-backed
// __htmlRewrite function that performs streaming HTML transformation.
func SetupHTMLRewriter(rt core.JSRuntime, _ *eventloop.EventLoop) error {
	// Register __htmlRewrite(htmlString) — transforms HTML using registered handlers.
	if err := rt.RegisterFunc("__htmlRewrite", func(htmlStr string) string {
		result, err := rewriteHTML(rt, htmlStr)
		if err != nil {
			// Return original HTML on error.
			return htmlStr
		}
		return result
	}); err != nil {
		return fmt.Errorf("registering __htmlRewrite: %w", err)
	}

	return rt.Eval(htmlRewriterJS)
}

// handlerSpec describes a registered selector handler from JS.
type handlerSpec struct {
	selector   *CompoundSelector
	handlerIdx int
}

// MatchedElement tracks state for elements matched by selectors during rewriting.
type MatchedElement struct {
	HandlerIdx    int
	Depth         int
	SkipContent   bool   // setInnerContent or remove was called
	Removed       bool   // element.remove() was called (skip end tag too)
	NewTagName    string // tagName changed by handler (for end tag rewriting)
	InnerContent  string // replacement content for setInnerContent
	AppendContent string // element.append() — emitted before end tag
	AfterContent  string // element.after() — emitted after end tag
}

// rewriteHTML tokenizes HTML, matches selectors, calls JS handlers, and
// applies mutations to produce transformed output.
func rewriteHTML(rt core.JSRuntime, htmlStr string) (string, error) {
	// Read handler registrations from JS globals.
	handlersCount, err := rt.EvalInt(`
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
		selStr, err := rt.EvalString(fmt.Sprintf(`globalThis.__htmlrw_handlers[%d].selector`, i))
		if err != nil {
			continue
		}
		sel := ParseCompoundSelector(selStr)
		specs = append(specs, handlerSpec{selector: sel, handlerIdx: i})
	}

	// Tokenize and transform.
	tokenizer := gohtml.NewTokenizer(strings.NewReader(htmlStr))
	var out strings.Builder

	var matchStack []*MatchedElement
	depth := 0

	// DOM context tracking for combinator matching.
	var elementStack []ElementInfo
	siblingMap := make(map[int][]ElementInfo)

	// needsContext is true if any spec uses combinators.
	needsContext := false
	for _, spec := range specs {
		if !spec.selector.IsSimple() {
			needsContext = true
			break
		}
	}

	// selectorMatches checks whether a spec matches the given element,
	// using context-aware matching for compound selectors.
	selectorMatches := func(spec handlerSpec, tagName string, attrs map[string]string) bool {
		if spec.selector.IsSimple() {
			return spec.selector.Subject().Matches(tagName, attrs)
		}
		var siblings []ElementInfo
		if needsContext {
			siblings = siblingMap[depth]
		}
		return spec.selector.MatchesWithContext(tagName, attrs, elementStack, siblings)
	}

	for {
		tt := tokenizer.Next()
		if tt == gohtml.ErrorToken {
			break
		}

		token := tokenizer.Token()

		switch tt {
		case gohtml.StartTagToken:
			isVoid := VoidElement(token.Data)
			depth++

			// If inside a removed/replaced element, skip everything.
			if ShouldSkipContent(matchStack, depth) {
				if isVoid {
					depth--
				}
				continue
			}

			attrs := HtmlAttrsToMap(token.Attr)

			// Check selectors.
			matched := false
			for _, spec := range specs {
				if !selectorMatches(spec, token.Data, attrs) {
					continue
				}
				mutations := callElementHandler(rt, spec.handlerIdx, token.Data, attrs)

				if mutations == nil {
					matched = true
					out.WriteString(token.String())
					if !isVoid {
						matchStack = append(matchStack, &MatchedElement{
							HandlerIdx: spec.handlerIdx,
							Depth:      depth,
						})
					}
					break
				}
				matched = true

				// Before content.
				out.WriteString(mutations.Before)

				if mutations.Removed {
					if !isVoid {
						matchStack = append(matchStack, &MatchedElement{
							HandlerIdx:   spec.handlerIdx,
							Depth:        depth,
							SkipContent:  true,
							Removed:      true,
							AfterContent: mutations.After,
						})
					} else {
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

					me := &MatchedElement{
						HandlerIdx:    spec.handlerIdx,
						Depth:         depth,
						NewTagName:    mutations.NewTagName,
						AppendContent: mutations.Append,
						AfterContent:  mutations.After,
					}
					if mutations.InnerContent != "" {
						me.SkipContent = true
						me.InnerContent = mutations.InnerContent
					}
					matchStack = append(matchStack, me)
				}
				break
			}

			if !matched {
				out.WriteString(token.String())
			}

			// Update DOM context.
			if needsContext {
				info := ElementInfo{TagName: token.Data, Attrs: attrs, Depth: depth}
				siblingMap[depth] = append(siblingMap[depth], info)
				if !isVoid {
					elementStack = append(elementStack, info)
					delete(siblingMap, depth+1)
				}
			}

			if isVoid {
				depth--
			}

		case gohtml.EndTagToken:
			var skipEndTag bool
			var afterContent string
			var rewrittenTag string
			for i := len(matchStack) - 1; i >= 0; i-- {
				me := matchStack[i]
				if me.Depth == depth {
					if me.InnerContent != "" {
						out.WriteString(me.InnerContent)
					}
					out.WriteString(me.AppendContent)
					skipEndTag = me.Removed
					afterContent = me.AfterContent
					rewrittenTag = me.NewTagName
					matchStack = append(matchStack[:i], matchStack[i+1:]...)
					break
				}
			}

			if needsContext && len(elementStack) > 0 && elementStack[len(elementStack)-1].Depth == depth {
				elementStack = elementStack[:len(elementStack)-1]
				delete(siblingMap, depth+1)
			}

			depth--

			if skipEndTag || ShouldSkipContent(matchStack, depth+1) {
				out.WriteString(afterContent)
				continue
			}

			if rewrittenTag != "" && rewrittenTag != token.Data {
				out.WriteString("</" + rewrittenTag + ">")
			} else {
				out.WriteString(token.String())
			}
			out.WriteString(afterContent)

		case gohtml.TextToken:
			if ShouldSkipContent(matchStack, depth) {
				continue
			}

			textContent := token.Data
			handled := false

			for _, me := range matchStack {
				if !me.SkipContent && depth >= me.Depth {
					mutations := callTextHandler(rt, me.HandlerIdx, textContent, false)
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

			docMut := callDocTextHandler(rt, textContent)
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
			if ShouldSkipContent(matchStack, depth) {
				continue
			}

			handled := false
			for _, me := range matchStack {
				if !me.SkipContent && depth >= me.Depth {
					mutations := callCommentHandler(rt, me.HandlerIdx, token.Data)
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
			if ShouldSkipContent(matchStack, depth) {
				continue
			}

			attrs := HtmlAttrsToMap(token.Attr)
			handled := false

			for _, spec := range specs {
				if !selectorMatches(spec, token.Data, attrs) {
					continue
				}
				mutations := callElementHandler(rt, spec.handlerIdx, token.Data, attrs)
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

			if needsContext {
				info := ElementInfo{TagName: token.Data, Attrs: attrs, Depth: depth + 1}
				siblingMap[depth+1] = append(siblingMap[depth+1], info)
			}

			if !handled {
				out.WriteString(token.String())
			}
		}
	}

	// Call document end handler.
	endMutations := callDocEndHandler(rt)
	if endMutations != "" {
		out.WriteString(endMutations)
	}

	return out.String(), nil
}

// ShouldSkipContent returns true if the current depth is inside a matched
// element that has had its content replaced (setInnerContent) or removed.
func ShouldSkipContent(stack []*MatchedElement, depth int) bool {
	for _, me := range stack {
		if me.SkipContent && depth >= me.Depth {
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
func callElementHandler(rt core.JSRuntime, handlerIdx int, tagName string, attrs map[string]string) *elementMutations {
	attrsJSON, _ := json.Marshal(attrs)

	if err := rt.SetGlobal("__el_tag", tagName); err != nil {
		return nil
	}
	if err := rt.SetGlobal("__el_attrs", string(attrsJSON)); err != nil {
		return nil
	}
	if err := rt.Eval(fmt.Sprintf("globalThis.__el_handler_idx = %d;", handlerIdx)); err != nil {
		return nil
	}

	result, err := rt.EvalString(`
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
func callTextHandler(rt core.JSRuntime, handlerIdx int, text string, last bool) *textMutations {
	if err := rt.SetGlobal("__text_content", text); err != nil {
		return nil
	}
	if err := rt.Eval(fmt.Sprintf("globalThis.__text_last = %t;", last)); err != nil {
		return nil
	}
	if err := rt.Eval(fmt.Sprintf("globalThis.__text_handler_idx = %d;", handlerIdx)); err != nil {
		return nil
	}

	result, err := rt.EvalString(`
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
func callCommentHandler(rt core.JSRuntime, handlerIdx int, comment string) *textMutations {
	if err := rt.SetGlobal("__comment_content", comment); err != nil {
		return nil
	}
	if err := rt.Eval(fmt.Sprintf("globalThis.__comment_handler_idx = %d;", handlerIdx)); err != nil {
		return nil
	}

	result, err := rt.EvalString(`
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
func callDocTextHandler(rt core.JSRuntime, text string) *textMutations {
	if err := rt.SetGlobal("__doc_text_content", text); err != nil {
		return nil
	}

	result, err := rt.EvalString(`
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
func callDocEndHandler(rt core.JSRuntime) string {
	result, err := rt.EvalString(`
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

// HtmlAttrsToMap converts html.Attribute slice to a string map.
func HtmlAttrsToMap(attrs []gohtml.Attribute) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Val
	}
	return m
}

// VoidElement returns true for HTML void elements that have no end tag.
func VoidElement(tag string) bool {
	switch strings.ToLower(tag) {
	case "area", "base", "br", "col", "embed", "hr", "img", "input",
		"link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}
