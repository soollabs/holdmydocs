package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpEditIn struct {
	Slug     string `json:"slug" jsonschema:"page slug, always namespace/page"`
	BaseHash string `json:"basehash" jsonschema:"required hash from read_page or the last successful edit/save; stale hashes are rejected"`
	Script   string `json:"script" jsonschema:"sed-style commands separated by semicolons or newlines, applied in order"`
}

type mcpEditOut struct {
	Slug    string `json:"slug"`
	Hash    string `json:"hash" jsonschema:"basehash for the next edit or save"`
	Changed bool   `json:"changed"`
}

func (app *App) registerMCPEditTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "edit_page",
		Description: "Edit an existing page's Markdown body without resending it; preserve title, tags and pin. Requires write access and basehash. " +
			"One atomic commit for the entire script; stale hashes fail, unchanged bodies produce no commit. Returns only slug, hash and changed. " +
			"Sed-style subset: optional positive line number, $, or /regexp/ address; inclusive address,address ranges; s/pattern/replacement/[g], d, a, i, c. " +
			"Commands separated by semicolons or newlines run in order on each input line; d and c end that line's processing. " +
			"Substitution replaces the first occurrence per selected line, or all with g; punctuation delimiters supported. " +
			"Regexps use Go regexp syntax (not sed BRE): (group), not \\(group\\). Replacements use & for the match, \\1 through \\9 for groups, \\& and \\\\ for literals, \\n and \\t for newline/tab; $ is literal. " +
			"a/i/c text consumes the rest of its script line (including semicolons); optional backslash-newline before text, backslash-newline continues text. " +
			"No other flags, commands, empty-pattern reuse, shell or file access. At most 128 commands; output is limited to the page body size limit.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpEditIn) (*mcp.CallToolResult, mcpEditOut, error) {
		if err := app.mcpRequireScope(ctx, scopeWrite); err != nil {
			return nil, mcpEditOut{}, err
		}
		if !validMCPPageSlug(in.Slug) {
			return nil, mcpEditOut{}, fmt.Errorf("invalid slug %q", in.Slug)
		}
		if err := app.mcpRequireSlug(ctx, in.Slug); err != nil {
			return nil, mcpEditOut{}, err
		}
		if in.BaseHash == "" {
			return nil, mcpEditOut{}, fmt.Errorf("basehash is required; read the target page first")
		}

		cfg := app.config()
		if cfg.SyncMode == "bidirectional" && cfg.Git.RemoteURL != "" {
			if _, err := app.Store.FetchAndFF(); err != nil {
				slog.Warn("mcp edit-time fetch", "slug", in.Slug, "err", err)
			}
		}
		file := pageFile(in.Slug)
		content, hash, err := app.Store.Read(file)
		if err != nil {
			return nil, mcpEditOut{}, err
		}
		if hash != in.BaseHash {
			return mcpEditConflict(hash), mcpEditOut{}, nil
		}
		page := ParsePage(in.Slug, content)
		body, err := applyPageEdits(page.Body, in.Script)
		if err != nil {
			return nil, mcpEditOut{}, err
		}
		if err := validatePageInput(page.Title, page.Tags, body); err != nil {
			return nil, mcpEditOut{}, err
		}
		changed := body != page.Body
		if changed {
			page.Body = body
			encoded := page.Encode()
			changed = !bytes.Equal(content, encoded)
			content = encoded
		}
		// Even no-ops go through the atomic hash check. SaveChecked skips identical
		// bytes, preserving original formatting and avoiding an empty commit.
		authorName, authorEmail := app.gitAuthor(app.mcpUser(ctx))
		hash, err = app.Store.SaveChecked(file, file, in.BaseHash, content, "Edit "+page.Title, authorName, authorEmail)
		if errors.Is(err, ErrConflict) {
			_, currentHash, readErr := app.Store.Read(file)
			if readErr != nil {
				return nil, mcpEditOut{}, fmt.Errorf("conflict: page changed or was deleted since basehash")
			}
			return mcpEditConflict(currentHash), mcpEditOut{}, nil
		}
		if err != nil {
			return nil, mcpEditOut{}, err
		}
		if changed {
			if err := app.Index.UpdatePage(page, hash); err != nil {
				return nil, mcpEditOut{}, err
			}
			slog.Info("mcp edited", "slug", in.Slug, "author", authorName)
		}
		return nil, mcpEditOut{Slug: in.Slug, Hash: hash, Changed: changed}, nil
	})
}

func mcpEditConflict(hash string) *mcp.CallToolResult {
	payload, _ := json.Marshal(map[string]string{"error": "conflict: page changed since basehash; re-read before retrying", "hash": hash})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(payload)}}}
}

// Page edits use a deliberately finite sed-style language, not an executable
// program. Regexps use Go syntax; replacements use sed's & and \1 through \9.
const maxEditCommands = 128

type editAddress struct {
	line int
	last bool
	re   *regexp.Regexp
}

func (a editAddress) matches(line, total int, text string) bool {
	switch {
	case a.re != nil:
		return a.re.MatchString(text)
	case a.last:
		return line == total
	default:
		return line == a.line
	}
}

type pageEditCommand struct {
	first, last *editAddress
	active      bool
	op          byte
	re          *regexp.Regexp
	replacement string
	global      bool
	text        string
}

type editParser struct {
	script string
	pos    int
}

func (p *editParser) spaces() {
	for p.pos < len(p.script) && (p.script[p.pos] == ' ' || p.script[p.pos] == '\t') {
		p.pos++
	}
}

// delimited preserves regexp escapes, consuming only escaped delimiters.
func (p *editParser) delimited(delim byte, replacement bool) (string, error) {
	var out strings.Builder
	for p.pos < len(p.script) {
		c := p.script[p.pos]
		p.pos++
		if c == delim {
			return out.String(), nil
		}
		if c == '\n' {
			return "", fmt.Errorf("unterminated edit expression")
		}
		if c == '\\' {
			if p.pos == len(p.script) {
				break
			}
			next := p.script[p.pos]
			p.pos++
			if next == delim {
				if replacement { // An escaped delimiter must remain literal, including &.
					out.WriteByte('\\')
					out.WriteByte(next)
				} else {
					out.WriteString(regexp.QuoteMeta(string(next)))
				}
			} else {
				out.WriteByte(c)
				out.WriteByte(next)
			}
		} else {
			out.WriteByte(c)
		}
	}
	return "", fmt.Errorf("unterminated edit expression")
}

func (p *editParser) address() (*editAddress, error) {
	p.spaces()
	if p.pos == len(p.script) {
		return nil, nil
	}
	switch c := p.script[p.pos]; {
	case c == '$':
		p.pos++
		return &editAddress{last: true}, nil
	case c == '/':
		p.pos++
		pattern, err := p.delimited('/', false)
		if err != nil {
			return nil, err
		}
		re, err := compileEditRegexp(pattern)
		return &editAddress{re: re}, err
	case c >= '0' && c <= '9':
		start := p.pos
		for p.pos < len(p.script) && p.script[p.pos] >= '0' && p.script[p.pos] <= '9' {
			p.pos++
		}
		n, err := strconv.Atoi(p.script[start:p.pos])
		if err != nil || n < 1 {
			return nil, fmt.Errorf("line addresses must be positive integers")
		}
		return &editAddress{line: n}, nil
	default:
		return nil, nil
	}
}

func compileEditRegexp(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, fmt.Errorf("empty regexp is not supported; specify the pattern")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid edit regexp: %w", err)
	}
	return re, nil
}

func parsePageEdits(script string) ([]pageEditCommand, error) {
	if len(script) > maxPageBodyBytes || !utf8.ValidString(script) {
		return nil, fmt.Errorf("edit script must be valid UTF-8 and at most %d bytes", maxPageBodyBytes)
	}
	p := editParser{script: script}
	var commands []pageEditCommand
	for {
		p.spaces()
		if p.pos == len(script) {
			break
		}
		if script[p.pos] == ';' || script[p.pos] == '\n' {
			p.pos++
			continue
		}
		if len(commands) == maxEditCommands {
			return nil, fmt.Errorf("edit script exceeds %d commands", maxEditCommands)
		}
		var cmd pageEditCommand
		var err error
		cmd.first, err = p.address()
		if err != nil {
			return nil, err
		}
		p.spaces()
		if p.pos < len(script) && script[p.pos] == ',' {
			if cmd.first == nil {
				return nil, fmt.Errorf("range requires a starting address")
			}
			p.pos++
			cmd.last, err = p.address()
			if err != nil {
				return nil, err
			}
			if cmd.last == nil {
				return nil, fmt.Errorf("range requires an ending address")
			}
		}
		p.spaces()
		if p.pos == len(script) {
			return nil, fmt.Errorf("missing edit command")
		}
		cmd.op = script[p.pos]
		p.pos++
		switch cmd.op {
		case 's':
			if p.pos == len(script) {
				return nil, fmt.Errorf("missing substitution delimiter")
			}
			delim := script[p.pos]
			p.pos++
			if delim < '!' || delim > '~' || delim == '\\' || (delim >= '0' && delim <= '9') || (delim >= 'A' && delim <= 'Z') || (delim >= 'a' && delim <= 'z') {
				return nil, fmt.Errorf("substitution delimiter must be ASCII punctuation other than backslash")
			}
			pattern, err := p.delimited(delim, false)
			if err != nil {
				return nil, err
			}
			cmd.re, err = compileEditRegexp(pattern)
			if err != nil {
				return nil, err
			}
			cmd.replacement, err = p.delimited(delim, true)
			if err != nil {
				return nil, err
			}
			if err := validateEditReplacement(cmd.replacement, cmd.re.NumSubexp(), delim); err != nil {
				return nil, err
			}
			if p.pos < len(script) && script[p.pos] == 'g' {
				cmd.global = true
				p.pos++
			}
		case 'd':
		case 'a', 'i', 'c':
			// Text consumes the rest of its script line. Backslash-newline continues it.
			p.spaces()
			if p.pos < len(script) && script[p.pos] == '\\' {
				p.pos++
				if p.pos < len(script) && script[p.pos] == '\n' {
					p.pos++
				}
			}
			var text strings.Builder
			for p.pos < len(script) && script[p.pos] != '\n' {
				c := script[p.pos]
				p.pos++
				if c == '\\' && p.pos < len(script) {
					c = script[p.pos]
					p.pos++
					if c == 'n' {
						c = '\n'
					}
				}
				text.WriteByte(c)
			}
			cmd.text = text.String()
		default:
			return nil, fmt.Errorf("unsupported edit command %q; supported: s, a, i, c, d", cmd.op)
		}
		p.spaces()
		if p.pos < len(script) && script[p.pos] != ';' && script[p.pos] != '\n' {
			return nil, fmt.Errorf("unsupported syntax at byte %d", p.pos+1)
		}
		commands = append(commands, cmd)
	}
	if len(commands) == 0 {
		return nil, fmt.Errorf("edit script is empty")
	}
	return commands, nil
}

func validateEditReplacement(s string, groups int, delim byte) error {
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			continue
		}
		i++
		if i == len(s) {
			return fmt.Errorf("unfinished replacement escape")
		}
		c := s[i]
		if c >= '1' && c <= '9' {
			if int(c-'0') > groups {
				return fmt.Errorf("replacement refers to missing capture group %c", c)
			}
		} else if c != '\\' && c != '&' && c != 'n' && c != 't' && c != delim {
			return fmt.Errorf("unsupported replacement escape \\%c", c)
		}
	}
	return nil
}

// selected implements inclusive sed ranges. A regexp end address is first
// tested on the line after the starting match, unlike numeric end addresses.
func (c *pageEditCommand) selected(line, total int, text string) (bool, bool) {
	if c.first == nil {
		return true, true
	}
	if c.last == nil {
		return c.first.matches(line, total, text), true
	}
	started := false
	if !c.active {
		if !c.first.matches(line, total, text) {
			return false, false
		}
		c.active = true
		started = true
	}
	end := line == total
	if c.last.re != nil {
		end = end || (!started && c.last.matches(line, total, text))
	} else {
		end = end || c.last.matches(line, total, text) || (c.last.line > 0 && line >= c.last.line)
	}
	if end {
		c.active = false
	}
	return true, end
}

func editWrite(out *strings.Builder, text string) error {
	if len(text) > maxPageBodyBytes-out.Len() {
		return fmt.Errorf("edited body exceeds %d bytes", maxPageBodyBytes)
	}
	out.WriteString(text)
	return nil
}

func (c pageEditCommand) substitute(text string) (string, error) {
	n := 1
	if c.global {
		n = -1
	}
	matches := c.re.FindAllStringSubmatchIndex(text, n)
	if len(matches) == 0 {
		return text, nil
	}
	var out strings.Builder
	end := 0
	for _, m := range matches {
		if err := editWrite(&out, text[end:m[0]]); err != nil {
			return "", err
		}
		for i := 0; i < len(c.replacement); i++ {
			value := c.replacement[i : i+1]
			switch c.replacement[i] {
			case '&':
				value = text[m[0]:m[1]]
			case '\\':
				i++
				ch := c.replacement[i]
				switch {
				case ch >= '1' && ch <= '9':
					group := int(ch-'0') * 2
					value = ""
					if m[group] >= 0 {
						value = text[m[group]:m[group+1]]
					}
				case ch == 'n':
					value = "\n"
				case ch == 't':
					value = "\t"
				default:
					value = c.replacement[i : i+1]
				}
			}
			if err := editWrite(&out, value); err != nil {
				return "", err
			}
		}
		end = m[1]
	}
	if err := editWrite(&out, text[end:]); err != nil {
		return "", err
	}
	return out.String(), nil
}

func applyPageEdits(body, script string) (string, error) {
	commands, err := parsePageEdits(script)
	if err != nil {
		return "", err
	}
	if len(body) > maxPageBodyBytes {
		return "", fmt.Errorf("page body exceeds edit limit")
	}
	if body == "" {
		return body, nil
	}
	trailing := strings.HasSuffix(body, "\n")
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	var out strings.Builder
	for index, text := range lines {
		newline := index < len(lines)-1 || trailing
		deleted := false
		var appended strings.Builder
		for i := range commands {
			cmd := &commands[i]
			selected, rangeEnd := cmd.selected(index+1, len(lines), text)
			if !selected {
				continue
			}
			switch cmd.op {
			case 's':
				text, err = cmd.substitute(text)
				if err != nil {
					return "", err
				}
			case 'i':
				if err := editWrite(&out, cmd.text+"\n"); err != nil {
					return "", err
				}
			case 'a':
				if err := editWrite(&appended, cmd.text+"\n"); err != nil {
					return "", err
				}
			case 'c':
				if rangeEnd {
					if err := editWrite(&out, cmd.text+"\n"); err != nil {
						return "", err
					}
				}
				deleted = true
			case 'd':
				deleted = true
			}
			if deleted {
				break
			}
		}
		if !deleted {
			if err := editWrite(&out, text); err != nil {
				return "", err
			}
			if newline || appended.Len() > 0 {
				if err := editWrite(&out, "\n"); err != nil {
					return "", err
				}
			}
		}
		if err := editWrite(&out, appended.String()); err != nil {
			return "", err
		}
	}
	return out.String(), nil
}
