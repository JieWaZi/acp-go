package acpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	acp "github.com/coder/acp-go-sdk"
)

var (
	// ErrInvalidAdapter 表示 Adapter 名称或工厂不满足注册约束。
	ErrInvalidAdapter = errors.New("invalid adapter")
	// ErrAdapterAlreadyRegistered 表示同名 Adapter 已存在，禁止静默覆盖既有实现。
	ErrAdapterAlreadyRegistered = errors.New("adapter already registered")
	// ErrAdapterNotFound 表示请求的 Adapter 没有注册，调用方必须显式处理而不能任意回退。
	ErrAdapterNotFound = errors.New("adapter not found")
)

// Factory 根据进程级上下文创建一个实现 SDK Agent 接口的具体 Adapter。
// 返回 SDK 接口是注册表连接多种实现与 acp-go-sdk 所必需的协议边界。
type Factory func(ctx context.Context) (acp.Agent, error)

// Registration 描述一个可选择的 Adapter 及其创建工厂。
type Registration struct {
	// Name 是命令行与诊断中使用的稳定 Adapter 名称。
	Name string
	// Factory 在 Adapter 被选中后创建其实例，未选中的工厂不会执行。
	Factory Factory
}

// Selection 保存一次已完成选择的元数据和具体 SDK Agent。
type Selection struct {
	// Name 是完成默认值解析后的 Adapter 名称。
	Name string
	// Agent 是交给 acp-go-sdk AgentSideConnection 的协议实现。
	Agent acp.Agent
}

// Registry 并发安全地保存 Adapter 注册项，并负责默认名称解析。
type Registry struct {
	// defaultName 是调用方省略 Adapter 名称时使用的稳定默认值。
	defaultName string
	// mu 保护 registrations，允许初始化后并发选择并保留安全的动态注册边界。
	mu sync.RWMutex
	// registrations 按规范化名称保存显式注册项。
	registrations map[string]Registration
}

// NewRegistry 创建使用 defaultName 的空注册表。
// 普通构造函数已经足够表达唯一必选参数，因此不引入无实际演进需求的函数式选项。
func NewRegistry(defaultName string) (*Registry, error) {
	defaultName = strings.TrimSpace(defaultName)
	if defaultName == "" {
		return nil, fmt.Errorf("creating registry: %w: default name is empty", ErrInvalidAdapter)
	}

	return &Registry{
		defaultName:   defaultName,
		registrations: make(map[string]Registration),
	}, nil
}

// Register 添加一个 Adapter 注册项。
// 重复名称会失败而不是覆盖，避免组合根配置错误在运行期悄然切换实现。
func (r *Registry) Register(registration Registration) error {
	name := strings.TrimSpace(registration.Name)
	if name == "" {
		return fmt.Errorf("registering adapter: %w: name is empty", ErrInvalidAdapter)
	}
	if registration.Factory == nil {
		return fmt.Errorf("registering adapter %q: %w: factory is nil", name, ErrInvalidAdapter)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.registrations[name]; exists {
		return fmt.Errorf("registering adapter %q: %w", name, ErrAdapterAlreadyRegistered)
	}

	registration.Name = name
	r.registrations[name] = registration
	return nil
}

// Select 解析默认名称、查找注册项并调用对应工厂。
// 未知名称必须在协议连接建立前失败，因此这里绝不回退到其他已注册实现。
func (r *Registry) Select(ctx context.Context, requestedName string) (*Selection, error) {
	name := strings.TrimSpace(requestedName)
	if name == "" {
		name = r.defaultName
	}

	r.mu.RLock()
	registration, exists := r.registrations[name]
	r.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("selecting adapter %q: %w", name, ErrAdapterNotFound)
	}

	agent, err := registration.Factory(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating adapter %q: %w", name, err)
	}
	if agent == nil {
		return nil, fmt.Errorf("creating adapter %q: %w: factory returned nil agent", name, ErrInvalidAdapter)
	}

	return &Selection{
		Name:  registration.Name,
		Agent: agent,
	}, nil
}
