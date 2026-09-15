package slugify

import "testing"

func TestSlugify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Sketchbook AA", "sketchbook-aa"},
		{"  Hello   World!  ", "hello-world"},
		{"café au lait", "cafe-au-lait"},
		{"Beyoncé's Résumé", "beyonce-s-resume"},
		{"C++ Programming", "c-programming"},
		{"What's New in 2024?", "what-s-new-in-2024"},
		{"url_slug_guide", "url-slug-guide"},
		{"--leading and trailing--", "leading-and-trailing"},
		{"", ""},
		{"微信", ""}, // 纯 CJK 折叠为空，由调用方兜底
	}
	for _, c := range cases {
		if got := Slugify(c.in); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugifyHash(t *testing.T) {
	// 正常输入与 Slugify 一致
	if got := SlugifyHash("Sketchbook AA"); got != "sketchbook-aa" {
		t.Errorf("SlugifyHash normal = %q, want %q", got, "sketchbook-aa")
	}
	// 空输入返回空串
	if got := SlugifyHash(""); got != "" {
		t.Errorf("SlugifyHash(\"\") = %q, want empty", got)
	}
	// 纯 CJK 输入哈希兜底，且确定性（同名同值）
	a, b := SlugifyHash("微信"), SlugifyHash("微信")
	if a == "" || a != b {
		t.Errorf("SlugifyHash CJK should be deterministic, got %q / %q", a, b)
	}
}

func TestPath(t *testing.T) {
	cases := []struct {
		segs []string
		want string
	}{
		{[]string{"app", "Sketchbook AA"}, "/app/sketchbook-aa"},
		{[]string{"app", ""}, ""},
		{[]string{"", ""}, ""},
	}
	for _, c := range cases {
		if got := Path(c.segs...); got != c.want {
			t.Errorf("Path(%v) = %q, want %q", c.segs, got, c.want)
		}
	}
}
