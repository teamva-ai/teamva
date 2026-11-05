package store

import (
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/teamva-ai/teamva/node/provider"
	"github.com/teamva-ai/teamva/node/provider/catalog"
	"github.com/teamva-ai/teamva/server/internal/domain"
)

const (
	defaultProjectSearchLimit = 30
	maxProjectSearchLimit     = 50
	maxProjectSearchExcerpt   = 240
)

func normalizeProjectSearch(query string, limit int) (string, int, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", 0, domain.Invalid("搜索内容不能为空")
	}
	if utf8.RuneCountInString(query) > 200 {
		return "", 0, domain.Invalid("搜索内容不能超过 200 个字符")
	}
	if limit == 0 {
		limit = defaultProjectSearchLimit
	}
	if limit < 1 || limit > maxProjectSearchLimit {
		return "", 0, domain.Invalid("limit 必须在 1 到 50 之间")
	}
	return strings.ToLower(query), limit, nil
}

func projectSearchMatch(text, normalizedQuery string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || containsInlineImageDataURL(text) {
		return "", false
	}
	// SQLite's lower()/NOCASE only fold ASCII. Use exactly the same Unicode
	// lowercasing for SQL candidates and the final match, and never LIKE/GLOB
	// user input. strings.Index also avoids the old O(text * query) rune loop.
	lower := strings.ToLower(text)
	index := strings.Index(lower, normalizedQuery)
	if index < 0 {
		return "", false
	}
	// Go's simple lowercase mapping preserves rune count, not byte count (e.g.
	// İ -> i), so map the offset by runes before slicing the original text.
	return projectSearchExcerpt([]rune(text), utf8.RuneCountInString(lower[:index]), utf8.RuneCountInString(normalizedQuery)), true
}

func projectSearchExcerpt(text []rune, matchIndex, matchLength int) string {
	start := matchIndex
	for start > 0 && !unicode.IsSpace(text[start-1]) && matchIndex-start < 70 {
		start--
	}
	end := matchIndex + matchLength
	for end < len(text) && !unicode.IsSpace(text[end]) && end-(matchIndex+matchLength) < 130 {
		end++
	}
	excerptRunes := []rune(strings.TrimSpace(string(text[start:end])))
	clipped := len(excerptRunes) > maxProjectSearchExcerpt
	if clipped {
		excerptRunes = excerptRunes[:maxProjectSearchExcerpt]
	}
	excerpt := strings.TrimSpace(string(excerptRunes))
	if start > 0 {
		excerpt = "…" + excerpt
	}
	if end < len(text) || clipped {
		excerpt += "…"
	}
	return excerpt
}

func visibleSearchRunInput(blocks []domain.ContentBlock) string {
	for _, block := range blocks {
		if block.Type != "text" || block.Internal {
			continue
		}
		text := strings.TrimSpace(block.Text)
		if text == "" || containsInlineImageDataURL(text) {
			continue
		}
		if !strings.HasPrefix(text, "# 任务（Issue）上下文") {
			return text
		}
		marker := "## 本次指令\n"
		start := strings.LastIndex(text, marker)
		if start < 0 {
			// Mention prompts contain the complete Issue context and no distinct
			// user-message boundary. The triggering public comment is searchable
			// separately, so fail closed instead of indexing control context.
			continue
		}
		value := text[start+len(marker):]
		for _, suffix := range []string{
			"\n\n请阅读上下文后开始工作", "\n\n## 当前工作回复", "\n\n## 持续目标模式",
		} {
			if index := strings.Index(value, suffix); index >= 0 {
				value = value[:index]
			}
		}
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

type visibleAssistantMessage struct {
	anchor     string
	text       string
	occurredAt time.Time
}

// One accumulator is scoped to one bounded, chronologically replayed Run
// window. Raw events are never retained. Replacements update an existing
// message, so superseded snapshots are not independently searchable.
type projectSearchAssistant struct {
	messages       []visibleAssistantMessage
	byAnchor       map[string]int
	current        strings.Builder
	currentID      string
	currentStarted time.Time
	retained       int
	limit          int
	truncated      bool
	projectOutput  func(string, map[string]any) provider.OutputUpdate
}

func newProjectSearchAssistant(limit int) *projectSearchAssistant {
	return &projectSearchAssistant{
		byAnchor: make(map[string]int), limit: limit,
		projectOutput: catalog.NewOutputProjection(MaxTerminalEventScanCount),
	}
}

func (a *projectSearchAssistant) resetCurrent() {
	a.current.Reset()
	a.currentID = ""
	a.currentStarted = time.Time{}
}

func (a *projectSearchAssistant) store(anchor, text string, occurred time.Time) {
	text = strings.TrimSpace(text)
	index, exists := a.byAnchor[anchor]
	previousBytes := 0
	if exists {
		previousBytes = len(a.messages[index].text)
	}
	if a.retained-previousBytes+len(text) > a.limit {
		a.truncated = true
		// Never search a clipped prefix or keep a superseded snapshot.
		text = ""
	}
	message := visibleAssistantMessage{anchor: anchor, text: text, occurredAt: occurred}
	if exists {
		a.messages[index] = message
	} else if text != "" {
		a.byAnchor[anchor] = len(a.messages)
		a.messages = append(a.messages, message)
	}
	a.retained += len(text) - previousBytes
}

func (a *projectSearchAssistant) commit(anchor string, fallback time.Time) {
	occurred := a.currentStarted
	if occurred.IsZero() {
		occurred = fallback
	}
	a.store(anchor, a.current.String(), occurred)
	a.resetCurrent()
}

func (a *projectSearchAssistant) consume(event domain.RunEvent) {
	if event.Type == "search.gap" {
		// A skipped event may have been an instruction or part of this message.
		// Do not manufacture a phrase by joining deltas across that gap.
		a.commit("assistant-before:"+event.ID, event.OccurredAt)
		return
	}
	if event.Type == "user.steer" {
		a.commit("assistant-before:"+event.ID, event.OccurredAt)
		return
	}
	if internal, present := event.Payload["internal"]; present && internal != false {
		return
	}
	output := a.projectOutput(event.Type, event.Payload)
	if output.Ignore {
		return
	}
	if output.Replace {
		if output.Text == "" {
			return
		}
		id := output.ID
		if id == "" {
			id = event.ID
		}
		streamed := strings.TrimSpace(a.current.String())
		if streamed != "" && ((a.currentID != "" && a.currentID == output.ID) ||
			strings.HasPrefix(output.Text, streamed) || strings.HasPrefix(streamed, output.Text)) {
			a.resetCurrent()
		} else {
			a.commit("assistant-before:"+event.ID, event.OccurredAt)
		}
		a.store("assistant:"+id, output.Text, event.OccurredAt)
		return
	}
	if output.Text == "" {
		return
	}
	if a.currentID != "" && output.ID != "" && a.currentID != output.ID {
		a.commit("assistant-before:"+event.ID, event.OccurredAt)
	}
	if a.retained+a.current.Len()+len(output.Text) > a.limit {
		a.truncated = true
		a.resetCurrent()
		return
	}
	if a.current.Len() == 0 {
		a.currentStarted = event.OccurredAt
		a.currentID = output.ID
	}
	a.current.WriteString(output.Text)
}

func (a *projectSearchAssistant) finish(runID string) []visibleAssistantMessage {
	a.commit("assistant-delta:"+runID, time.Time{})
	return a.messages
}

func projectSearchLess(left, right domain.ProjectSearchResult) bool {
	if !left.OccurredAt.Equal(right.OccurredAt) {
		return left.OccurredAt.After(right.OccurredAt)
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return left.ID < right.ID
}

// Retain only the latest K, not every hit. The independent byte bound also
// covers unusually large titles (there is no legacy title-length constraint).
type projectSearchResults struct {
	items     []domain.ProjectSearchResult
	limit     int
	maxBytes  int
	bytes     int
	truncated bool
}

func projectSearchResultBytes(item domain.ProjectSearchResult) int {
	return len(item.ID) + len(item.Kind) + len(item.ProjectID) + len(item.IssueID) +
		len(item.MemoryID) + len(item.CommentID) + len(item.IssueAgentInstanceID) +
		len(item.RunID) + len(item.Role) + len(item.Anchor) + len(item.Title) + len(item.Excerpt)
}

func (r *projectSearchResults) add(item domain.ProjectSearchResult) {
	index := sort.Search(len(r.items), func(i int) bool { return projectSearchLess(item, r.items[i]) })
	if index >= r.limit || projectSearchResultBytes(item) > r.maxBytes {
		r.truncated = true
		return
	}
	r.items = append(r.items, domain.ProjectSearchResult{})
	copy(r.items[index+1:], r.items[index:])
	r.items[index] = item
	r.bytes += projectSearchResultBytes(item)
	for len(r.items) > r.limit || r.bytes > r.maxBytes {
		last := len(r.items) - 1
		r.bytes -= projectSearchResultBytes(r.items[last])
		r.items[last] = domain.ProjectSearchResult{}
		r.items = r.items[:last]
		r.truncated = true
	}
}

func (r *projectSearchResults) page() domain.ProjectSearchPage {
	if r.items == nil {
		r.items = []domain.ProjectSearchResult{}
	}
	return domain.ProjectSearchPage{Items: r.items, Truncated: r.truncated}
}
