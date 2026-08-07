package importer

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

const (
	MaxFileBytes = 10 << 20
	MaxItems     = 20_000
)

type Item struct {
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Note      string    `json:"note"`
	Tags      []string  `json:"tags"`
	Unread    bool      `json:"unread"`
	Starred   bool      `json:"starred"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

type Mapping struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Note      string `json:"note"`
	Tags      string `json:"tags"`
	Unread    string `json:"unread"`
	Starred   string `json:"starred"`
	CreatedAt string `json:"createdAt"`
}

type Result struct {
	Format  string   `json:"format"`
	Headers []string `json:"headers,omitempty"`
	Mapping Mapping  `json:"mapping"`
	Items   []Item   `json:"items"`
	Rows    int      `json:"rows"`
	Skipped int      `json:"skipped"`
}

func Parse(filename string, reader io.Reader, mapping Mapping) (Result, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxFileBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("读取导入文件: %w", err)
	}
	if len(data) > MaxFileBytes {
		return Result{}, fmt.Errorf("导入文件不能超过 10 MB")
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		decoded, _, decodeErr := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), data)
		if decodeErr != nil || !utf8.Valid(decoded) {
			return Result{}, fmt.Errorf("文件不是有效的 UTF-8 或 GB18030 编码")
		}
		data = decoded
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Result{}, fmt.Errorf("导入文件为空")
	}

	extension := strings.ToLower(filepath.Ext(filename))
	trimmed := bytes.TrimSpace(data)
	switch {
	case extension == ".json" || trimmed[0] == '{' || trimmed[0] == '[':
		return parseJSON(data)
	case extension == ".html" || extension == ".htm" || bytes.Contains(bytes.ToUpper(data), []byte("NETSCAPE-BOOKMARK-FILE")):
		return parseHTML(data)
	case extension == ".csv":
		return parseCSV(data, mapping)
	default:
		return Result{}, fmt.Errorf("无法识别文件格式，请使用 JSON、HTML 或 CSV")
	}
}

func parseJSON(data []byte) (Result, error) {
	type envelope struct {
		Bookmarks []Item `json:"bookmarks"`
	}
	var items []Item
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		if err := json.Unmarshal(data, &items); err != nil {
			return Result{}, fmt.Errorf("解析 JSON: %w", err)
		}
	} else {
		var payload envelope
		if err := json.Unmarshal(data, &payload); err != nil {
			return Result{}, fmt.Errorf("解析 JSON: %w", err)
		}
		items = payload.Bookmarks
	}
	return finish("json", nil, Mapping{}, items)
}

func parseHTML(data []byte) (Result, error) {
	tokenizer := html.NewTokenizer(bytes.NewReader(data))
	items := make([]Item, 0)
	var current Item
	inAnchor := false
	inDescription := false
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			if err := tokenizer.Err(); err != nil && err != io.EOF {
				return Result{}, fmt.Errorf("解析书签 HTML: %w", err)
			}
			return finish("html", nil, Mapping{}, items)
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			switch strings.ToLower(token.Data) {
			case "a":
				inDescription = false
				inAnchor = true
				current = Item{}
				for _, attribute := range token.Attr {
					switch strings.ToLower(attribute.Key) {
					case "href":
						current.URL = strings.TrimSpace(attribute.Val)
					case "tags":
						current.Tags = splitTags(attribute.Val)
					case "add_date":
						if unix, err := strconv.ParseInt(attribute.Val, 10, 64); err == nil && unix > 0 {
							current.CreatedAt = time.Unix(unix, 0).UTC()
						}
					}
				}
			case "dd":
				inDescription = len(items) > 0
			case "dt", "dl", "h3":
				inDescription = false
			}
		case html.EndTagToken:
			token := tokenizer.Token()
			switch strings.ToLower(token.Data) {
			case "a":
				inAnchor = false
				current.Title = cleanText(current.Title)
				items = append(items, current)
			case "dd":
				inDescription = false
			}
		case html.TextToken:
			text := string(tokenizer.Text())
			if inAnchor {
				current.Title += text
			} else if inDescription && len(items) > 0 {
				items[len(items)-1].Note += " " + text
				items[len(items)-1].Note = cleanText(items[len(items)-1].Note)
			}
		}
	}
}

func parseCSV(data []byte, mapping Mapping) (Result, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return Result{}, fmt.Errorf("解析 CSV: %w", err)
	}
	if len(records) == 0 {
		return Result{}, fmt.Errorf("CSV 没有表头")
	}
	headers := make([]string, len(records[0]))
	for index, header := range records[0] {
		headers[index] = strings.TrimSpace(header)
	}
	if mapping.URL == "" {
		mapping = inferMapping(headers)
	}
	indexes := headerIndexes(headers)
	items := make([]Item, 0, len(records)-1)
	skipped := 0
	for _, record := range records[1:] {
		item := Item{
			URL:     csvValue(record, indexes, mapping.URL),
			Title:   csvValue(record, indexes, mapping.Title),
			Note:    csvValue(record, indexes, mapping.Note),
			Tags:    splitTags(csvValue(record, indexes, mapping.Tags)),
			Unread:  parseBool(csvValue(record, indexes, mapping.Unread)),
			Starred: parseBool(csvValue(record, indexes, mapping.Starred)),
		}
		if rawTime := csvValue(record, indexes, mapping.CreatedAt); rawTime != "" {
			item.CreatedAt = parseTime(rawTime)
		}
		if strings.TrimSpace(item.URL) == "" {
			skipped++
			continue
		}
		items = append(items, item)
	}
	result, err := finish("csv", headers, mapping, items)
	result.Rows = max(0, len(records)-1)
	result.Skipped += skipped
	return result, err
}

func finish(format string, headers []string, mapping Mapping, items []Item) (Result, error) {
	if len(items) > MaxItems {
		return Result{}, fmt.Errorf("一次最多导入 %d 条书签", MaxItems)
	}
	cleaned := make([]Item, 0, len(items))
	skipped := 0
	for _, item := range items {
		item.URL = strings.TrimSpace(item.URL)
		item.Title = cleanText(item.Title)
		item.Note = cleanText(item.Note)
		item.Tags = normalizeTags(item.Tags)
		if item.URL == "" {
			skipped++
			continue
		}
		cleaned = append(cleaned, item)
	}
	return Result{Format: format, Headers: headers, Mapping: mapping, Items: cleaned, Rows: len(items), Skipped: skipped}, nil
}

func inferMapping(headers []string) Mapping {
	aliases := map[string][]string{
		"url":        {"url", "href", "link", "网址", "链接"},
		"title":      {"title", "name", "标题", "名称"},
		"note":       {"note", "notes", "description", "备注", "描述"},
		"tags":       {"tags", "tag", "labels", "标签"},
		"unread":     {"unread", "read_later", "later", "稍后阅读"},
		"starred":    {"starred", "favorite", "favourite", "收藏"},
		"created_at": {"created_at", "createdat", "add_date", "date", "创建时间"},
	}
	find := func(key string) string {
		for _, header := range headers {
			normalized := normalizeHeader(header)
			for _, alias := range aliases[key] {
				if normalized == normalizeHeader(alias) {
					return header
				}
			}
		}
		return ""
	}
	return Mapping{
		URL: find("url"), Title: find("title"), Note: find("note"), Tags: find("tags"),
		Unread: find("unread"), Starred: find("starred"), CreatedAt: find("created_at"),
	}
}

func headerIndexes(headers []string) map[string]int {
	indexes := make(map[string]int, len(headers))
	for index, header := range headers {
		indexes[header] = index
	}
	return indexes
}

func csvValue(record []string, indexes map[string]int, header string) string {
	index, ok := indexes[header]
	if !ok || index < 0 || index >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[index])
}

func normalizeHeader(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func cleanText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func splitTags(value string) []string {
	return normalizeTags(strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；'
	}))
}

func normalizeTags(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on", "是", "未读", "稍后阅读", "收藏":
		return true
	default:
		return false
	}
}

func parseTime(value string) time.Time {
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil && unix > 0 {
		return time.Unix(unix, 0).UTC()
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}
