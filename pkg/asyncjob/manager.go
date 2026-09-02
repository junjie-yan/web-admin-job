package asyncjob

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Manager 任务持久化管理器（基于原生 SQL，所有方法可并发调用）
type Manager struct {
	db  *sql.DB
	now func() int64
}

// NewManager 创建任务管理器，db 必须已连接到包含 async_task 表的数据库
func NewManager(db *sql.DB) *Manager {
	return &Manager{db: db, now: func() int64 { return time.Now().Unix() }}
}

// EnsureTable 幂等创建 async_task 表
func (m *Manager) EnsureTable(ctx context.Context) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS async_task (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(255) NOT NULL DEFAULT '' COMMENT '任务名称',
  type VARCHAR(32) NOT NULL DEFAULT '' COMMENT '任务类型: import/export/batch_update',
  biz_module VARCHAR(64) NOT NULL DEFAULT '' COMMENT '业务模块',
  status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT '状态: pending/processing/success/failed/partial/canceled',
  total_count BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '总条数',
  success_count BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '成功条数',
  fail_count BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '失败条数',
  progress TINYINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '进度 0-100',
  file_url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '输入文件 URL',
  file_name VARCHAR(255) NOT NULL DEFAULT '' COMMENT '原始文件名',
  result_file_url VARCHAR(512) NOT NULL DEFAULT '' COMMENT '结果文件 URL',
  error_message TEXT COMMENT '整体错误信息',
  error_detail MEDIUMTEXT COMMENT '错误明细 JSON, 前 200 条',
  operator_id VARCHAR(36) NOT NULL DEFAULT '' COMMENT '操作人 ID',
  operator_name VARCHAR(64) NOT NULL DEFAULT '' COMMENT '操作人名称',
  started_at BIGINT UNSIGNED DEFAULT NULL COMMENT '开始时间戳(秒)',
  finished_at BIGINT UNSIGNED DEFAULT NULL COMMENT '完成时间戳(秒)',
  created_at BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '创建时间戳(秒)',
  updated_at BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '更新时间戳(秒)',
  KEY idx_status (status),
  KEY idx_operator (operator_id),
  KEY idx_biz (biz_module, type),
  KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='异步任务表';`
	_, err := m.db.ExecContext(ctx, ddl)
	return err
}

// Create 插入一条 pending 状态的任务，返回新任务 ID
func (m *Manager) Create(ctx context.Context, in CreateInput) (uint64, error) {
	now := m.now()
	res, err := m.db.ExecContext(ctx, `
INSERT INTO async_task
  (name, type, biz_module, status, file_url, file_name, operator_id, operator_name, created_at, updated_at)
VALUES
  (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Type, in.BizModule, StatusPending,
		in.FileURL, in.FileName, in.OperatorID, in.OperatorName,
		now, now)
	if err != nil {
		return 0, fmt.Errorf("asyncjob: insert task: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(id), nil
}

// GetByID 查询单个任务（含错误明细）
func (m *Manager) GetByID(ctx context.Context, id uint64) (*Task, error) {
	row := m.db.QueryRowContext(ctx, selectColumns+` WHERE id = ?`, id)
	t, err := scanTaskDetail(row)
	if err != nil {
		return nil, fmt.Errorf("asyncjob: get task %d: %w", id, err)
	}
	return t, nil
}

// List 分页查询任务列表（不带 error_detail）
func (m *Manager) List(ctx context.Context, f ListFilter) (*ListResult, error) {
	if f.Page == 0 {
		f.Page = 1
	}
	if f.PageSize == 0 {
		f.PageSize = 20
	}
	if f.PageSize > 100 {
		f.PageSize = 100
	}

	var (
		where []string
		args  []interface{}
	)
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.BizModule != "" {
		where = append(where, "biz_module = ?")
		args = append(args, f.BizModule)
	}
	if f.Type != "" {
		where = append(where, "type = ?")
		args = append(args, f.Type)
	}
	if f.OperatorID != "" {
		where = append(where, "operator_id = ?")
		args = append(args, f.OperatorID)
	}
	if f.Keyword != "" {
		where = append(where, "(name LIKE ? OR file_name LIKE ?)")
		args = append(args, "%"+f.Keyword+"%", "%"+f.Keyword+"%")
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var total uint64
	if err := m.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM async_task`+whereSQL, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("asyncjob: count tasks: %w", err)
	}

	offset := (f.Page - 1) * f.PageSize
	query := listColumns + whereSQL + ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, f.PageSize, offset)
	rows, err := m.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("asyncjob: list tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var data []*Task
	for rows.Next() {
		t, err := scanTaskList(rows)
		if err != nil {
			return nil, fmt.Errorf("asyncjob: scan task: %w", err)
		}
		data = append(data, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &ListResult{Total: total, Page: f.Page, Size: f.PageSize, Data: data}, nil
}

// Cancel 标记任务取消（仅 pending/processing 可取消）
func (m *Manager) Cancel(ctx context.Context, id uint64, reason string) error {
	_, err := m.db.ExecContext(ctx,
		`UPDATE async_task SET status=?, error_message=?, finished_at=?, updated_at=? WHERE id=? AND status IN (?,?)`,
		StatusCanceled, reason, m.now(), m.now(), id, StatusPending, StatusProcessing)
	return err
}

// Delete 物理删除任务（仅终态可删）
func (m *Manager) Delete(ctx context.Context, id uint64) error {
	_, err := m.db.ExecContext(ctx,
		`DELETE FROM async_task WHERE id=? AND status IN (?,?,?,?,?)`,
		id, StatusSuccess, StatusFailed, StatusPartial, StatusCanceled, StatusPending)
	return err
}

// IsCanceled 判断任务是否已被取消（消费端在长任务中定期检查）
func (m *Manager) IsCanceled(ctx context.Context, id uint64) (bool, error) {
	var status string
	err := m.db.QueryRowContext(ctx, `SELECT status FROM async_task WHERE id = ?`, id).Scan(&status)
	if err != nil {
		return false, err
	}
	return status == StatusCanceled, nil
}

// MarkProcessing 标记任务为处理中（消费端开始执行时调用）
// 仅当当前状态为 pending 时才会成功，避免重复消费
func (m *Manager) MarkProcessing(ctx context.Context, id uint64) error {
	now := m.now()
	res, err := m.db.ExecContext(ctx,
		`UPDATE async_task SET status=?, started_at=COALESCE(started_at, ?), progress=?, updated_at=? WHERE id=? AND status=?`,
		StatusProcessing, now, 1, now, id, StatusPending)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("asyncjob: task %d not in pending status (possibly picked up by another worker or canceled)", id)
	}
	return nil
}

// UpdateProgress 更新进度（消费端每批次处理后调用）
// 同时累加 success/fail 计数，更新 total 与 progress
// errorDetailAppend 不为空时，作为一条错误明细追加到 error_detail JSON 数组中（最多保留前 200 条）
func (m *Manager) UpdateProgress(ctx context.Context, in ProgressUpdate) error {
	now := m.now()

	// 1) 先读取当前 error_detail（仅当需要追加时）
	var existingDetails []string
	if in.ErrorDetailAppend != "" {
		var raw sql.NullString
		if err := m.db.QueryRowContext(ctx, `SELECT error_detail FROM async_task WHERE id = ?`, in.ID).Scan(&raw); err != nil {
			return err
		}
		if raw.Valid && raw.String != "" && raw.String != "null" {
			_ = json.Unmarshal([]byte(raw.String), &existingDetails) // 容错：解析失败则忽略
		}
		existingDetails = append(existingDetails, in.ErrorDetailAppend)
		if len(existingDetails) > 200 {
			existingDetails = existingDetails[len(existingDetails)-200:]
		}
	}

	var (
		query string
		args  []interface{}
	)
	if in.ErrorDetailAppend != "" {
		buf, _ := json.Marshal(existingDetails)
		query = `UPDATE async_task SET total_count=?, success_count=?, fail_count=?, progress=?, error_detail=?, updated_at=? WHERE id=?`
		args = []interface{}{in.TotalCount, in.SuccessCount, in.FailCount, in.Progress, string(buf), now, in.ID}
	} else {
		query = `UPDATE async_task SET total_count=?, success_count=?, fail_count=?, progress=?, updated_at=? WHERE id=?`
		args = []interface{}{in.TotalCount, in.SuccessCount, in.FailCount, in.Progress, now, in.ID}
	}
	_, err := m.db.ExecContext(ctx, query, args...)
	return err
}

// Finish 标记任务完成（终态）。status 必须是 success/failed/partial
// 会写入 finished_at 与最终统计、整体错误信息、结果文件 URL
func (m *Manager) Finish(ctx context.Context, in FinishInput) error {
	if in.Status != StatusSuccess && in.Status != StatusFailed && in.Status != StatusPartial {
		return fmt.Errorf("asyncjob: invalid finish status %q", in.Status)
	}
	progress := uint8(100)
	if in.Status == StatusFailed {
		progress = m.computeProgress(in.TotalCount, in.SuccessCount)
	}
	now := m.now()
	_, err := m.db.ExecContext(ctx, `
UPDATE async_task
SET status=?, total_count=?, success_count=?, fail_count=?, progress=?,
    error_message=COALESCE(NULLIF(?, ''), error_message),
    result_file_url=COALESCE(NULLIF(?, ''), result_file_url),
    finished_at=?, updated_at=?
WHERE id=?`,
		in.Status, in.TotalCount, in.SuccessCount, in.FailCount, progress,
		in.ErrorMessage, in.ResultFileURL,
		now, now, in.ID)
	return err
}

// SetResultFile 单独更新结果文件 URL（适用于流式任务，最终 Finish 之前先写入）
func (m *Manager) SetResultFile(ctx context.Context, id uint64, resultFileURL string) error {
	_, err := m.db.ExecContext(ctx,
		`UPDATE async_task SET result_file_url=?, updated_at=? WHERE id=?`,
		resultFileURL, m.now(), id)
	return err
}

// computeProgress 估算失败场景下的进度（避免终态 failed 但 progress 仍为 0）
// 没有 total 或全失败时返回 100，否则按 (success/total) 计算
func (m *Manager) computeProgress(total, success uint64) uint8 {
	if total == 0 {
		return 100
	}
	p := success * 100 / total
	if p > 100 {
		p = 100
	}
	if p < 100 && p > 90 {
		return 90
	}
	return uint8(p)
}

// scanner 通用扫描接口
type scanner interface {
	Scan(dest ...interface{}) error
}

const (
	selectColumns = `SELECT id, name, type, biz_module, status, total_count, success_count, fail_count,
progress, file_url, file_name, result_file_url, error_message, error_detail,
operator_id, operator_name, started_at, finished_at, created_at, updated_at FROM async_task`
	listColumns = `SELECT id, name, type, biz_module, status, total_count, success_count, fail_count,
progress, file_url, file_name, result_file_url, error_message,
operator_id, operator_name, started_at, finished_at, created_at, updated_at FROM async_task`
)

func scanTaskDetail(s scanner) (*Task, error) {
	t := &Task{}
	var (
		startedAt  sql.NullInt64
		finishedAt sql.NullInt64
		errMsg     sql.NullString
		errDetail  sql.NullString
	)
	err := s.Scan(
		&t.ID, &t.Name, &t.Type, &t.BizModule, &t.Status,
		&t.TotalCount, &t.SuccessCount, &t.FailCount, &t.Progress,
		&t.FileURL, &t.FileName, &t.ResultFileURL,
		&errMsg, &errDetail,
		&t.OperatorID, &t.OperatorName,
		&startedAt, &finishedAt, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if startedAt.Valid {
		v := startedAt.Int64
		t.StartedAt = &v
	}
	if finishedAt.Valid {
		v := finishedAt.Int64
		t.FinishedAt = &v
	}
	if errMsg.Valid {
		t.ErrorMessage = errMsg.String
	}
	if errDetail.Valid {
		t.ErrorDetail = errDetail.String
	}
	return t, nil
}

func scanTaskList(s scanner) (*Task, error) {
	t := &Task{}
	var (
		startedAt  sql.NullInt64
		finishedAt sql.NullInt64
		errMsg     sql.NullString
	)
	err := s.Scan(
		&t.ID, &t.Name, &t.Type, &t.BizModule, &t.Status,
		&t.TotalCount, &t.SuccessCount, &t.FailCount, &t.Progress,
		&t.FileURL, &t.FileName, &t.ResultFileURL, &errMsg,
		&t.OperatorID, &t.OperatorName,
		&startedAt, &finishedAt, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if startedAt.Valid {
		v := startedAt.Int64
		t.StartedAt = &v
	}
	if finishedAt.Valid {
		v := finishedAt.Int64
		t.FinishedAt = &v
	}
	if errMsg.Valid {
		t.ErrorMessage = errMsg.String
	}
	return t, nil
}
