package search

import (
	"reflect"
	"testing"
)

func TestTokensChineseBigramsAndLatinWords(t *testing.T) {
	got := Tokens("SQLite 中文搜索")
	want := []string{"sqlite", "中文", "文搜", "搜索"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokens = %#v, want %#v", got, want)
	}
}

func TestTokensNormalizesWidthAndCase(t *testing.T) {
	got := Tokens("ＣｌｏｕｄＦｌａｒｅ WORKERS")
	want := []string{"cloudflare", "workers"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokens = %#v, want %#v", got, want)
	}
}

func TestQueryUsesPrefixesForNonChineseTokens(t *testing.T) {
	got := Query("tome 中文")
	want := `"tome"* AND "中文"`
	if got != want {
		t.Fatalf("Query = %q, want %q", got, want)
	}
}
