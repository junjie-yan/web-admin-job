package appdetail

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// ---------- 最小化 SQL 假驱动 ----------
// 按 SQL 子串匹配返回脚本化结果集，并记录 Exec 语句与绑定参数；
// 避免为单测引入第三方 sqlmock 依赖。单测串行执行，脚本通过包级变量挂载。

const fakeDriverName = "appdetail_fake"

// scriptedQuery 一条脚本化查询：queryContains 为 SQL 子串匹配条件；
// args 非 nil 时同时比对绑定参数（database/sql 已按 DefaultParameterConverter 归一化为 int64/string 等）
type scriptedQuery struct {
	queryContains string
	args          []driver.Value
	columns       []string
	rows          [][]driver.Value
}

// execRecord 记录一次 Exec 的 SQL 与绑定参数
type execRecord struct {
	query string
	args  []driver.Value
}

// fakeScript 当前测试挂载的脚本（查询脚本 + Exec 记录）
type fakeScript struct {
	queries []scriptedQuery
	execs   []execRecord
}

var currentFake *fakeScript

func init() { sql.Register(fakeDriverName, fakeDriver{}) }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{}, nil }

type fakeConn struct{}

func (*fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake: Prepare 未实现（请走 QueryerContext/ExecerContext 路径）")
}
func (*fakeConn) Close() error              { return nil }
func (*fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("fake: 事务未实现") }

// QueryContext 按脚本返回结果集；未脚本化的查询直接报错，避免静默空结果掩盖问题
func (*fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if currentFake == nil {
		return nil, errors.New("fake: 未挂载脚本")
	}
	got := namedValues(args)
	for _, q := range currentFake.queries {
		if !strings.Contains(query, q.queryContains) {
			continue
		}
		if q.args != nil && !reflect.DeepEqual(got, q.args) {
			continue
		}
		return &fakeRows{columns: q.columns, rows: q.rows}, nil
	}
	return nil, fmt.Errorf("fake: 未脚本化的查询: %s args=%v", query, got)
}

// ExecContext 记录语句与参数，返回影响 1 行
func (*fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if currentFake == nil {
		return nil, errors.New("fake: 未挂载脚本")
	}
	currentFake.execs = append(currentFake.execs, execRecord{query: query, args: namedValues(args)})
	return driver.RowsAffected(1), nil
}

type fakeRows struct {
	columns []string
	rows    [][]driver.Value
	idx     int
}

func (r *fakeRows) Columns() []string { return r.columns }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.idx])
	r.idx++
	return nil
}

func namedValues(args []driver.NamedValue) []driver.Value {
	vals := make([]driver.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, a.Value)
	}
	return vals
}

// newFakeDB 挂载脚本并打开假驱动连接；测试结束自动卸载脚本并关闭连接
func newFakeDB(t *testing.T, queries ...scriptedQuery) (*sql.DB, *fakeScript) {
	t.Helper()
	s := &fakeScript{queries: queries}
	currentFake = s
	db, err := sql.Open(fakeDriverName, "")
	if err != nil {
		t.Fatalf("打开假驱动失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		currentFake = nil
	})
	return db, s
}

// siteQuery 站点域名查询脚本：任意域名命中 site_id=7
func siteQuery() scriptedQuery {
	return scriptedQuery{
		queryContains: "FROM site WHERE domain",
		columns:       []string{"id"},
		rows:          [][]driver.Value{{int64(7)}},
	}
}

// updateRow 构造批量更新模板的一行（23 列），set 按列索引填值
func updateRow(set map[int]string) []string {
	row := make([]string, uColShortDesc+1)
	for col, v := range set {
		row[col] = v
	}
	return row
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// ---------- 测试用例 ----------

// TestParseUpdateRowDomainIDStatus 核心场景：站点域名 + 记录ID + 状态=1 应解析成功，
// 且 UpdateByID 生成仅含 status 的更新语句（绑定 1）并按记录 ID 定位
func TestParseUpdateRowDomainIDStatus(t *testing.T) {
	db, _ := newFakeDB(t, siteQuery())
	nr := NewResolver(context.Background(), db)

	r, err := ParseRow(2, updateRow(map[int]string{
		uColSite:   "www.example.com",
		uColID:     "123",
		uColStatus: "1",
	}), nr, ParseModeUpdate)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if r == nil {
		t.Fatal("期望返回非空行")
	}
	if r.ID != 123 || r.SiteID != 7 || r.SiteDomain != "www.example.com" {
		t.Fatalf("定位字段不符: ID=%d SiteID=%d SiteDomain=%q", r.ID, r.SiteID, r.SiteDomain)
	}
	if r.Status == nil || *r.Status != 1 {
		t.Fatalf("期望 status=1（启用），实际 %v", r.Status)
	}
	if !r.hasUpdatableFields() {
		t.Fatal("仅填状态列也应视为有效更新字段")
	}

	// UpdateByID：SET 子句仅含 status 与 updated_at，绑定参数为 [1, 123]
	edb, script := newFakeDB(t)
	if err := UpdateByID(context.Background(), edb, r.ID, r); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if len(script.execs) != 1 {
		t.Fatalf("期望执行 1 条 UPDATE，实际 %d", len(script.execs))
	}
	e := script.execs[0]
	const wantSQL = "UPDATE app_detail SET status=?,updated_at=NOW() WHERE id=?"
	if e.query != wantSQL {
		t.Fatalf("UPDATE 语句不符:\n got: %s\nwant: %s", e.query, wantSQL)
	}
	if !reflect.DeepEqual(e.args, []driver.Value{int64(1), int64(123)}) {
		t.Fatalf("绑定参数不符: %v", e.args)
	}
}

// TestParseUpdateRowStatusValues 状态列取值映射（与前端 1=启用/0=禁用 约定一致）：
// 1/启用/其他未知值→1；0/2/禁用/disable/ban/false→0；留空→不更新该列
func TestParseUpdateRowStatusValues(t *testing.T) {
	db, _ := newFakeDB(t, siteQuery())
	nr := NewResolver(context.Background(), db)

	cases := []struct {
		cell string
		want uint8
	}{
		{"1", 1}, {"启用", 1}, {"enable", 1},
		{"0", 0}, {"2", 0}, {"禁用", 0}, {"disable", 0}, {"ban", 0}, {"false", 0},
	}
	for _, c := range cases {
		r, err := ParseRow(2, updateRow(map[int]string{uColSite: "a.com", uColID: "9", uColStatus: c.cell}), nr, ParseModeUpdate)
		if err != nil {
			t.Fatalf("status=%q 解析失败: %v", c.cell, err)
		}
		if r.Status == nil || *r.Status != c.want {
			t.Fatalf("status=%q 期望 %d，实际 %v", c.cell, c.want, r.Status)
		}
	}

	r, err := ParseRow(2, updateRow(map[int]string{uColSite: "a.com", uColID: "9", uColStatus: "  ", uColName: "App"}), nr, ParseModeUpdate)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if r.Status != nil {
		t.Fatalf("状态留空不应更新该列，实际 %d", *r.Status)
	}
}

// TestParseUpdateRowErrors 必填与格式校验：缺站点域名 / 缺记录ID / ID 非法 / 无更新字段 / 数值列非法 / 站点不存在；空行返回 nil
func TestParseUpdateRowErrors(t *testing.T) {
	db, _ := newFakeDB(t, siteQuery())
	nr := NewResolver(context.Background(), db)

	cases := []struct {
		name string
		row  map[int]string
		want string
	}{
		{"缺站点域名", map[int]string{uColID: "9", uColStatus: "1"}, "站点域名 必填"},
		{"缺记录ID", map[int]string{uColSite: "a.com", uColStatus: "1"}, "记录 ID 必填"},
		{"ID非数字", map[int]string{uColSite: "a.com", uColID: "abc", uColStatus: "1"}, "记录 ID 无效"},
		{"ID为0", map[int]string{uColSite: "a.com", uColID: "0", uColStatus: "1"}, "记录 ID 无效"},
		{"无更新字段", map[int]string{uColSite: "a.com", uColID: "9"}, "至少填写一项"},
		{"下载量非数字", map[int]string{uColSite: "a.com", uColID: "9", uColDownloads: "1k"}, "无法解析为整数"},
	}
	for _, c := range cases {
		_, err := ParseRow(2, updateRow(c.row), nr, ParseModeUpdate)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: 期望错误包含 %q，实际 %v", c.name, c.want, err)
		}
	}

	r, err := ParseRow(2, updateRow(nil), nr, ParseModeUpdate)
	if err != nil || r != nil {
		t.Fatalf("空行应返回 nil, nil，实际 %v, %v", r, err)
	}

	// 站点不存在：域名查询无结果
	db2, _ := newFakeDB(t, scriptedQuery{queryContains: "FROM site WHERE domain", columns: []string{"id"}})
	nr2 := NewResolver(context.Background(), db2)
	_, err = ParseRow(2, updateRow(map[int]string{uColSite: "nope.com", uColID: "9", uColStatus: "1"}), nr2, ParseModeUpdate)
	if err == nil || !strings.Contains(err.Error(), "站点不存在") {
		t.Fatalf("期望站点不存在错误，实际 %v", err)
	}
}

// TestParseUpdateRowCategory 分类列按行内站点即时解析：名称查库转 ID，纯数字直接作为 ID，不存在的名称该行报错
func TestParseUpdateRowCategory(t *testing.T) {
	const catQuery = "FROM app_category WHERE site_id = ? AND level = ? AND name = ?"
	db, _ := newFakeDB(t,
		siteQuery(),
		scriptedQuery{queryContains: catQuery, args: []driver.Value{int64(7), int64(1), "Games"}, columns: []string{"id"}, rows: [][]driver.Value{{int64(55)}}},
		scriptedQuery{queryContains: catQuery, args: []driver.Value{int64(7), int64(1), "Nope"}, columns: []string{"id"}},
	)
	nr := NewResolver(context.Background(), db)

	r, err := ParseRow(3, updateRow(map[int]string{
		uColSite:      "a.com",
		uColID:        "9",
		uColFirstCat:  "Games",
		uColSecondCat: "66",
	}), nr, ParseModeUpdate)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if r.FirstCategoryID == nil || *r.FirstCategoryID != 55 {
		t.Fatalf("一级分类名称应解析为 55，实际 %v", r.FirstCategoryID)
	}
	if r.SecondCategoryID == nil || *r.SecondCategoryID != 66 {
		t.Fatalf("二级分类纯数字应直接作为 ID 66，实际 %v", r.SecondCategoryID)
	}

	_, err = ParseRow(4, updateRow(map[int]string{uColSite: "a.com", uColID: "9", uColFirstCat: "Nope"}), nr, ParseModeUpdate)
	if err == nil || !strings.Contains(err.Error(), "不存在该一级分类") {
		t.Fatalf("期望分类不存在错误，实际 %v", err)
	}
}

// TestParseUpdateRowColumnLayout 锁定更新模板列序：各字段落到对应列（较新增模板整体后移一位）
func TestParseUpdateRowColumnLayout(t *testing.T) {
	db, _ := newFakeDB(t, siteQuery())
	nr := NewResolver(context.Background(), db)

	r, err := ParseRow(2, updateRow(map[int]string{
		uColSite: "a.com", uColID: "9",
		uColName: "App", uColURL: "/app/app", uColLogo: "https://l", uColDeveloper: "Dev", uColVersion: "1.2",
		uColPrice: "Free", uColMinAndroid: "5.0", uColDownloads: "1000", uColRating: "4.5", uColReviewCnt: "12",
		uColDesc: "desc", uColGooglePlay: "https://gp", uColAppleStore: "https://as", uColAPKURL: "https://apk",
		uColAPKVersion: "1.2.3", uColAPKSize: "10MB", uColAPKUpdated: "2025-01-01", uColStatus: "0", uColShortDesc: "short",
	}), nr, ParseModeUpdate)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	strCases := []struct {
		name string
		got  *string
		want string
	}{
		{"URL", r.URL, "/app/app"}, {"Logo", r.Logo, "https://l"}, {"Developer", r.Developer, "Dev"},
		{"Version", r.Version, "1.2"}, {"Price", r.Price, "Free"}, {"MinAndroid", r.MinAndroid, "5.0"},
		{"Description", r.Description, "desc"}, {"GooglePlayURL", r.GooglePlayURL, "https://gp"},
		{"AppleStoreURL", r.AppleStoreURL, "https://as"}, {"APKDownloadURL", r.APKDownloadURL, "https://apk"},
		{"APKVersion", r.APKVersion, "1.2.3"}, {"APKSize", r.APKSize, "10MB"}, {"APKUpdated", r.APKUpdated, "2025-01-01"},
		{"ShortDesc", r.ShortDesc, "short"},
	}
	for _, c := range strCases {
		if c.got == nil || *c.got != c.want {
			t.Fatalf("%s 期望 %q，实际 %s", c.name, c.want, derefStr(c.got))
		}
	}
	if r.Name != "App" {
		t.Fatalf("Name 期望 App，实际 %q", r.Name)
	}
	if r.Downloads == nil || *r.Downloads != 1000 || r.Rating == nil || *r.Rating != 4.5 || r.ReviewCount == nil || *r.ReviewCount != 12 {
		t.Fatalf("数值列不符: downloads=%v rating=%v reviews=%v", r.Downloads, r.Rating, r.ReviewCount)
	}
	if r.Status == nil || *r.Status != 0 {
		t.Fatalf("状态 0 应解析为禁用，实际 %v", r.Status)
	}
}

// TestUpdateRowIdentifier 结果文件行标识：站点域名#记录ID 优先，缺一取其一，都缺时回退应用名称
func TestUpdateRowIdentifier(t *testing.T) {
	cases := []struct {
		row  map[int]string
		want string
	}{
		{map[int]string{uColSite: "a.com", uColID: "9"}, "a.com#9"},
		{map[int]string{uColSite: "a.com"}, "a.com"},
		{map[int]string{uColID: "9"}, "9"},
		{map[int]string{uColName: "App"}, "App"},
		{nil, "-"},
	}
	for _, c := range cases {
		if got := UpdateRowIdentifier(updateRow(c.row)); got != c.want {
			t.Fatalf("标识期望 %q，实际 %q", c.want, got)
		}
	}
}
