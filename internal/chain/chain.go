package chain

import (
	"context"
	"fmt"
	"strings"
)

// Decision 定义决策
type Decision struct {
	Continue   bool
	StopReason string
}

// Next 创建下一个决策
func Next() Decision {
	return Decision{Continue: true}
}

// Stop 创建停止决策
func Stop(reason string) Decision {
	return Decision{
		Continue:   false,
		StopReason: strings.TrimSpace(reason),
	}
}

// Handler 定义处理器
type Handler[T any] interface {
	Name() string
	Handle(context.Context, *T) (Decision, error)
}

// FuncHandler 定义函数处理器
type FuncHandler[T any] struct {
	HandlerName string
	HandlerFunc func(context.Context, *T) (Decision, error)
}

// Name 获取处理器名称
func (h FuncHandler[T]) Name() string {
	return strings.TrimSpace(h.HandlerName)
}

// Handle 处理状态
func (h FuncHandler[T]) Handle(ctx context.Context, state *T) (Decision, error) {
	if h.HandlerFunc == nil {
		return Decision{}, fmt.Errorf("handler func is nil")
	}
	return h.HandlerFunc(ctx, state)
}

// Run 运行处理器链
func Run[T any](ctx context.Context, label string, state *T, handlers ...Handler[T]) (Decision, error) {
	// 创建处理器链标签
	chainLabel := strings.TrimSpace(label)
	if chainLabel == "" {
		chainLabel = "chain"
	}

	// 创建决策
	decision := Next()
	// 遍历处理器
	for _, handler := range handlers {
		if ctx.Err() != nil {
			return Decision{}, ctx.Err()
		}
		if handler == nil {
			return Decision{}, fmt.Errorf("%s handler is nil", chainLabel)
		}
		// 获取处理器名称
		handlerName := strings.TrimSpace(handler.Name())
		if handlerName == "" {
			handlerName = "unnamed_handler"
		}

		// 处理状态
		nextDecision, err := handler.Handle(ctx, state)
		if err != nil {
			return nextDecision, fmt.Errorf("%s handler %s: %w", chainLabel, handlerName, err)
		}
		// 更新决策
		decision = nextDecision
		// 如果决策不继续，则返回
		if !decision.Continue {
			return decision, nil
		}
	}
	// 返回决策
	return decision, nil
}
