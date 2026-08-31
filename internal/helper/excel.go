package helper

import (
	"bytes"
	"fmt"
	"io"
	"strconv"

	"github.com/xuri/excelize/v2"
)

// ExcelImporter Excel 导入解析器（从字节流加载，提供行迭代与单元格读取）
type ExcelImporter struct {
	file      *excelize.File
	sheetName string
}

// NewExcelImporter 从字节流创建 Excel 导入解析器
func NewExcelImporter(data []byte) (*ExcelImporter, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("excel: open reader: %w", err)
	}
	sheetName := f.GetSheetName(0)
	return &ExcelImporter{file: f, sheetName: sheetName}, nil
}

// Close 关闭文件句柄
func (i *ExcelImporter) Close() error {
	if i.file == nil {
		return nil
	}
	return i.file.Close()
}

// GetRows 获取所有数据行（跳过表头），每行为单元格字符串切片
func (i *ExcelImporter) GetRows() ([][]string, error) {
	rows, err := i.file.GetRows(i.sheetName)
	if err != nil {
		return nil, fmt.Errorf("excel: get rows: %w", err)
	}
	if len(rows) <= 1 {
		return [][]string{}, nil
	}
	return rows[1:], nil
}

// ExcelWriter Excel 结果文件写入器（用于生成错误明细或导出文件）
type ExcelWriter struct {
	file      *excelize.File
	sheetName string
	rowIdx    int // 下一个写入行号（从 1 开始）
}

// NewExcelWriter 创建新的 ExcelWriter，并写入表头
// sheetName 为空时使用 "Sheet1"
func NewExcelWriter(sheetName string, headers []string) *ExcelWriter {
	if sheetName == "" {
		sheetName = "Sheet1"
	}
	f := excelize.NewFile()
	sheet := f.GetSheetName(0)
	if sheet != sheetName {
		// 重命名默认 sheet
		_ = f.SetSheetName(sheet, sheetName)
	}
	w := &ExcelWriter{file: f, sheetName: sheetName, rowIdx: 1}
	if len(headers) > 0 {
		w.WriteRow(headers)
	}
	return w
}

// WriteRow 写入一行（按列顺序填入），自动递增行号
func (w *ExcelWriter) WriteRow(values []string) error {
	for colIdx, val := range values {
		cell, err := excelize.CoordinatesToCellName(colIdx+1, w.rowIdx)
		if err != nil {
			return err
		}
		if err := w.file.SetCellValue(w.sheetName, cell, val); err != nil {
			return err
		}
	}
	w.rowIdx++
	return nil
}

// Bytes 将当前文件序列化为字节流（xlsx 格式）
func (w *ExcelWriter) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := w.file.WriteTo(io.Writer(&buf)); err != nil {
		return nil, fmt.Errorf("excel: serialize: %w", err)
	}
	return buf.Bytes(), nil
}

// Close 释放底层资源
func (w *ExcelWriter) Close() error {
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}

// ParseInt 将字符串解析为 int，空字符串返回 nil
func ParseInt(s string) (*int, error) {
	s = trimSpace(s)
	if s == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil, fmt.Errorf("无法解析为整数: %s", s)
	}
	return &v, nil
}

// ParseInt64 将字符串解析为 int64，空字符串返回 nil
func ParseInt64(s string) (*int64, error) {
	s = trimSpace(s)
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("无法解析为整数: %s", s)
	}
	return &v, nil
}

// ParseUint64 将字符串解析为 uint64，空字符串返回 nil
func ParseUint64(s string) (*uint64, error) {
	s = trimSpace(s)
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("无法解析为正整数: %s", s)
	}
	return &v, nil
}

// ParseFloat64 将字符串解析为 float64，空字符串返回 nil
func ParseFloat64(s string) (*float64, error) {
	s = trimSpace(s)
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, fmt.Errorf("无法解析为浮点数: %s", s)
	}
	return &v, nil
}

func trimSpace(s string) string {
	// 去除首尾空白与不可见字符
	var b []byte
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == 0xa0 || r == 0x200b {
			continue
		}
		b = append(b, []byte(string(r))...)
	}
	return string(b)
}
