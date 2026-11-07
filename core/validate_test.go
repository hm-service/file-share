package core

import (
	"fmt"
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

func TestNormalizeGroupName(t *testing.T) {
	maxBytes := strings.Repeat("a", MaxGroupNameBytes)
	tooLong := strings.Repeat("a", MaxGroupNameBytes+1)
	chinese := strings.Repeat("组", 21) // 21 × 3 = 63 字节

	cases := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "空串", raw: "", want: "", ok: true},
		{name: "只有空白", raw: " \t\n ", want: "", ok: true},
		{name: "去首尾空白", raw: "  工作  ", want: "工作", ok: true},
		{name: "64 字节", raw: maxBytes, want: maxBytes, ok: true},
		{name: "65 字节", raw: tooLong},
		{name: "中文 63 字节", raw: chinese, want: chinese, ok: true},
		{name: "含 NUL", raw: "a\x00b"},
		{name: "含换行", raw: "a\nb"},
		{name: "含 DEL", raw: "a\x7fb"},
		{name: "中间带制表符", raw: "a\tb"},
		{name: "首尾制表符被 trim 后合法", raw: "\tabc\t", want: "abc", ok: true},
		{name: "含路径分隔符", raw: "a/b"},
		{name: "单点", raw: "."},
		{name: "双点", raw: ".."},
		{name: "以 .html 结尾", raw: "a.html"},
		{name: "页面表占用 auth", raw: "auth"},
		{name: "接口前缀占用 api", raw: "api"},
		{name: "下载前缀占用 d", raw: "d"},
		{name: "静态资源占用 assets", raw: "assets"},
		{name: "图标占用 favicon.svg", raw: "favicon.svg"},
		{name: "前端视图占用 favorites", raw: "favorites"},
		{name: "前端视图占用 ungrouped", raw: "ungrouped"},
		{name: "含点但可路由", raw: "v1.2", want: "v1.2", ok: true},
		{name: "含空格与井号", raw: "设计 #2", want: "设计 #2", ok: true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, ok := NormalizeGroupName(item.raw)
			if ok != item.ok {
				t.Fatalf("NormalizeGroupName(%q) ok = %v，想要 %v", item.raw, ok, item.ok)
			}
			if ok && got != item.want {
				t.Errorf("NormalizeGroupName(%q) = %q，想要 %q", item.raw, got, item.want)
			}
		})
	}
}

func TestValidOrderItems(t *testing.T) {
	makeItems := func(count int) []OrderItem {
		items := make([]OrderItem, 0, count)
		for i := 0; i < count; i++ {
			items = append(items, OrderItem{ID: fmt.Sprintf("id-%d", i), SortOrder: int64(i)})
		}
		return items
	}
	cases := []struct {
		name  string
		items []OrderItem
		want  bool
	}{
		{name: "空列表", items: nil},
		{name: "单项", items: []OrderItem{{ID: "a", SortOrder: 0}}, want: true},
		{name: "2000 项", items: makeItems(MaxOrderItems), want: true},
		{name: "超过 2000 项", items: makeItems(MaxOrderItems + 1)},
		{name: "重复 id", items: []OrderItem{{ID: "a", SortOrder: 1}, {ID: "a", SortOrder: 2}}},
		{name: "空 id", items: []OrderItem{{ID: "", SortOrder: 1}}},
		{name: "负数顺序", items: []OrderItem{{ID: "a", SortOrder: -1}}},
		{name: "顺序上界", items: []OrderItem{{ID: "a", SortOrder: MaxSortOrder}}, want: true},
		{name: "顺序超过上界", items: []OrderItem{{ID: "a", SortOrder: MaxSortOrder + 1}}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := ValidOrderItems(item.items); got != item.want {
				t.Errorf("ValidOrderItems() = %v，想要 %v", got, item.want)
			}
		})
	}
}
