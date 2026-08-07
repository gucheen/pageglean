package importer

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

func TestParsePageGleanJSON(t *testing.T) {
	result, err := Parse("bookmarks.json", strings.NewReader(`{
		"bookmarks":[{"url":"https://example.com/a","title":"示例","tags":["中文"]}]
	}`), Mapping{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != "json" || len(result.Items) != 1 || result.Items[0].Tags[0] != "中文" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestParseNetscapeHTML(t *testing.T) {
	input := `<!DOCTYPE NETSCAPE-Bookmark-file-1><DL><p>
	<DT><A HREF="https://example.com/a" ADD_DATE="1700000000" TAGS="产品,中文">测试文章</A>
	<DD>稍后仔细阅读
	</DL><p>`
	result, err := Parse("bookmarks.html", strings.NewReader(input), Mapping{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Note != "稍后仔细阅读" || len(result.Items[0].Tags) != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestParseCSVInfersAndOverridesMapping(t *testing.T) {
	input := "网址,名称,标签,收藏\nhttps://example.com/a,文章,中文;产品,是\n"
	result, err := Parse("bookmarks.csv", strings.NewReader(input), Mapping{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mapping.URL != "网址" || len(result.Items) != 1 || !result.Items[0].Starred {
		t.Fatalf("unexpected result: %#v", result)
	}

	override := "link,caption\nhttps://example.com/b,手动映射\n"
	result, err = Parse("custom.csv", strings.NewReader(override), Mapping{URL: "link", Title: "caption"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Items[0].Title != "手动映射" {
		t.Fatalf("unexpected override: %#v", result)
	}
}

func TestParseGB18030CSV(t *testing.T) {
	encoded, _, err := transform.Bytes(simplifiedchinese.GB18030.NewEncoder(), []byte("网址,标题\nhttps://example.com/gb,中文书签\n"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Parse("gb.csv", bytes.NewReader(encoded), Mapping{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].Title != "中文书签" {
		t.Fatalf("unexpected GB18030 result: %#v", result)
	}
}
