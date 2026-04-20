package dailysim

import (
	"fmt"
	"strings"

	"github.com/FangcunMount/seeddata-runner/internal/seedruntime"
)

// dailySimulationStateStore 定义每日模拟用户状态存储
type dailySimulationStateStore struct {
	file seedruntime.JSONStateFile[dailySimulationDaemonState]
}

// newDailySimulationStateStore 创建每日模拟用户状态存储
func newDailySimulationStateStore(rawPath string) dailySimulationStateStore {
	path := normalizeDailySimulationStateFile(rawPath)
	return dailySimulationStateStore{
		file: seedruntime.NewJSONStateFile[dailySimulationDaemonState](path),
	}
}

// Path 返回每日模拟用户状态存储路径
func (s dailySimulationStateStore) Path() string {
	return s.file.Path()
}

// Load 加载每日模拟用户状态
func (s dailySimulationStateStore) Load() (*dailySimulationDaemonState, error) {
	state, err := s.file.Load()
	if err != nil {
		return nil, fmt.Errorf("daily simulation daemon state store load: %w", err)
	}
	return state, nil
}

// Save 保存每日模拟用户状态
func (s dailySimulationStateStore) Save(state *dailySimulationDaemonState) error {
	if state == nil {
		return fmt.Errorf("daily simulation daemon state is nil")
	}
	if err := s.file.Save(state); err != nil {
		return fmt.Errorf("daily simulation daemon state store save: %w", err)
	}
	return nil
}

// normalizeDailySimulationStateFile 规范化每日模拟用户状态文件路径
func normalizeDailySimulationStateFile(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = dailySimulationDaemonDefaultStateFile
	}
	return raw
}
