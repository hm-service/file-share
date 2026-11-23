package i18n

import (
	"strings"
	"testing"
)

// contractKeys 是契约里规定的全部后端文案键：缺任何一个都会让前端拿到键名本身。
var contractKeys = []string{
	"auth.required",
	"auth.invalid",
	"auth.expired",
	"auth.malformed",
	"permission.denied",
	"request.json",
	"request.too-big",
	"file.not-found",
	"file.invalid-path",
	"download.invalid",
	"download.expired",
	"download.invalid-signature",
	"share.invalid-ttl",
	"store.files",
	"store.groups",
	"store.favorites",
	"internal",
	"storage.unavailable",
	"not-found",
	"method-not-allowed",
	"session.issue",
}

func TestCatalogCoversContractKeys(t *testing.T) {
	for _, key := range contractKeys {
		item, ok := catalog[key]
		if !ok {
			t.Errorf("缺少文案键 %q", key)
			continue
		}
		if strings.TrimSpace(item.zh) == "" {
			t.Errorf("%q 缺少中文文案", key)
		}
		if strings.TrimSpace(item.en) == "" {
			t.Errorf("%q 缺少英文文案", key)
		}
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   Lang
	}{
		{name: "空", header: "", want: ZhHans},
		{name: "英文", header: "en", want: En},
		{name: "英文地区", header: "en-US,en;q=0.9", want: En},
		{name: "中文", header: "zh-CN", want: ZhHans},
		{name: "中文繁体也当中文", header: "zh-TW", want: ZhHans},
		{name: "按出现顺序取第一个", header: "fr-FR, en-US, zh-CN", want: En},
		{name: "按出现顺序取第一个（反序）", header: "fr-FR, zh-CN, en-US", want: ZhHans},
		{name: "认不出就用默认", header: "de-DE, fr-FR", want: ZhHans},
		{name: "带权重参数", header: "de;q=1.0, en;q=0.5", want: En},
		{name: "大小写不敏感", header: "EN-gb", want: En},
		{name: "空前缀不算", header: ",,,", want: ZhHans},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := Parse(item.header); got != item.want {
				t.Errorf("Parse(%q) = %q，想要 %q", item.header, got, item.want)
			}
		})
	}
}

func TestT(t *testing.T) {
	if got := T(ZhHans, "auth.required"); got != "需要登录" {
		t.Errorf("中文文案 = %q", got)
	}
	if got := T(En, "auth.required"); got != "Sign-in required" {
		t.Errorf("英文文案 = %q", got)
	}
	// 未知键原样返回，不做 fallback —— 漏配要一眼可见。
	if got := T(ZhHans, "no.such.key"); got != "no.such.key" {
		t.Errorf("未知键 = %q，想要键名本身", got)
	}
	if got := T(Lang("fr"), "auth.required"); got != "需要登录" {
		t.Errorf("不认识的语言应当回源语言，得到 %q", got)
	}
}
