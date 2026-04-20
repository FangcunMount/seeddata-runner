package seedruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// JSONStateFile 定义 JSON 状态文件存储
type JSONStateFile[T any] struct {
	path string
}

// NewJSONStateFile 创建 JSON 状态文件存储
func NewJSONStateFile[T any](path string) JSONStateFile[T] {
	return JSONStateFile[T]{path: path}
}

// Path 返回状态文件路径
func (s JSONStateFile[T]) Path() string {
	return s.path
}

// Load 加载状态文件；文件不存在时返回零值状态
func (s JSONStateFile[T]) Load() (*T, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			var zero T
			return &zero, nil
		}
		return nil, fmt.Errorf("read state file %s: %w", s.path, err)
	}
	var state T
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode state file %s: %w", s.path, err)
	}
	return &state, nil
}

// Save 保存状态文件
func (s JSONStateFile[T]) Save(state *T) error {
	if state == nil {
		return fmt.Errorf("state is nil")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create state dir for %s: %w", s.path, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state file %s: %w", s.path, err)
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write state file %s: %w", s.path, err)
	}
	return nil
}
