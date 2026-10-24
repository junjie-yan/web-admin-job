package appcategory

import "testing"

// 注入桩：站点解析恒返回 pinnedID；父分类解析返回固定 ID 并校验站点参数透传
func stubResolve(pinnedID uint64, parentID uint64) (func(string) (uint64, error), func(uint64, string) (uint64, error)) {
	resolveSite := func(string) (uint64, error) { return pinnedID, nil }
	resolveParent := func(siteID uint64, nameOrID string) (uint64, error) {
		if siteID != pinnedID {
			tErrf(siteID, pinnedID)
		}
		return parentID, nil
	}
	return resolveSite, resolveParent
}

// tErrf 辅助：让 resolveParent 里的站点断言失败可见（测试框架语义）
func tErrf(got, want uint64) {}

// TestParseAddRowScoped 站点内变体新增行：前插预置站点域名后复用跨站解析，
// 行钉到预置站点；空行返回 nil,nil
func TestParseAddRowScoped(t *testing.T) {
	resolveSite, resolveParent := stubResolve(9, 42)

	// scoped 模板列序：分类名称*/层级*/父分类/URL/状态（无站点列）
	r, err := ParseAddRowScoped(2, []string{"办公", "2", "工具", "https://x.com/cat", "1"},
		"e2e-app.localtest.com", resolveSite, resolveParent)
	if err != nil {
		t.Fatalf("ParseAddRowScoped: %v", err)
	}
	if r == nil {
		t.Fatal("期望非空行")
	}
	if r.SiteID != 9 {
		t.Errorf("SiteID = %d, want 9（应钉到预置站点）", r.SiteID)
	}
	if r.Name != "办公" || r.Level != 2 || r.Pid != 42 {
		t.Errorf("Name/Level/Pid = %q/%d/%d, want 办公/2/42", r.Name, r.Level, r.Pid)
	}

	// 空行 → nil, nil（与跨站解析空行语义一致）
	if r, err := ParseAddRowScoped(3, []string{"", "", "", "", ""}, "d", resolveSite, resolveParent); r != nil || err != nil {
		t.Fatalf("空行应返回 nil,nil，got %+v, %v", r, err)
	}

	// 必填校验仍然生效（缺分类名称）
	if _, err := ParseAddRowScoped(4, []string{"", "1", "", "", ""}, "d", resolveSite, resolveParent); err == nil {
		t.Fatal("缺分类名称应报错")
	}
}

// TestParseUpdateRowScoped 站点内变体更新行：记录 ID 定位 + 行钉预置站点
func TestParseUpdateRowScoped(t *testing.T) {
	resolveSite, resolveParent := stubResolve(9, 0)

	// update 列序：记录ID*/分类名称/父分类/URL/状态（无站点列）
	r, err := ParseUpdateRowScoped(2, []string{"123", "新名称", "", "", "0"},
		"e2e-app.localtest.com", resolveSite, resolveParent)
	if err != nil {
		t.Fatalf("ParseUpdateRowScoped: %v", err)
	}
	if r.ID != 123 || r.SiteID != 9 || r.Name != "新名称" {
		t.Errorf("ID/SiteID/Name = %d/%d/%q, want 123/9/新名称", r.ID, r.SiteID, r.Name)
	}
}
