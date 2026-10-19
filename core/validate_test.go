package core

import (
	"strings"
	"testing"
)

func TestIsSHA256(t *testing.T) {
	valid := strings.Repeat("ab12", 16)
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "正常", value: valid, want: true},
		{name: "全数字", value: strings.Repeat("0", 64), want: true},
		{name: "全 f", value: strings.Repeat("f", 64), want: true},
		{name: "空串", value: ""},
		{name: "太短", value: valid[:63]},
		{name: "太长", value: valid + "a"},
		{name: "大写", value: strings.Repeat("AB", 32)},
		{name: "非十六进制", value: strings.Repeat("zz", 32)},
		{name: "带连字符", value: strings.Repeat("a", 32) + "-" + strings.Repeat("a", 31)},
		{name: "带空格", value: strings.Repeat("a", 63) + " "},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := IsSHA256(item.value); got != item.want {
				t.Errorf("IsSHA256(%q) = %v，想要 %v", item.value, got, item.want)
			}
		})
	}
}

func TestValidFilename(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    bool
		comment string
	}{
		{name: "普通文件名", value: "hello.txt", want: true},
		{name: "带空格", value: "my report 2026.pdf", want: true},
		{name: "中文", value: "分析报告.docx", want: true},
		{name: "点开头", value: ".gitignore", want: true},
		{name: "255 字节", value: strings.Repeat("a", 255), want: true},
		{name: "256 字节", value: strings.Repeat("a", 256)},
		{name: "中文 255 字节", value: strings.Repeat("好", 85), want: true},
		{name: "中文超限", value: strings.Repeat("好", 86)},
		{name: "空串", value: ""},
		{name: "单个点", value: "."},
		{name: "两个点", value: ".."},
		{name: "带斜杠", value: "dir/file.txt"},
		{name: "带反斜杠", value: `dir\file.txt`},
		{name: "含 NUL", value: "a\x00b"},
		{name: "含换行", value: "a\nb"},
		{name: "含 DEL", value: "a\x7fb"},
		{name: "非法 UTF-8", value: string([]byte{0xff, 0xfe})},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := ValidFilename(item.value); got != item.want {
				t.Errorf("ValidFilename(%q) = %v，想要 %v", item.value, got, item.want)
			}
		})
	}
}

func TestErrorShape(t *testing.T) {
	err := Fail(404, "file.not-found")
	if err.Status != 404 || err.Key != "file.not-found" {
		t.Errorf("Fail() = %+v", err)
	}
	if err.Error() != "file.not-found" {
		t.Errorf("Error() = %q，想要键名", err.Error())
	}
}
