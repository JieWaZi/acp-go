package acpserver

import (
	"context"
	"errors"
	"testing"

	acp "github.com/coder/acp-go-sdk"
)

// registryTestAgent 是注册表测试使用的非 nil SDK Agent 令牌。
// 测试不会调用其协议方法，因此嵌入接口只用于满足工厂返回类型，不模拟 SDK 行为。
type registryTestAgent struct {
	// Agent 提供注册表边界所要求的方法集，具体协议行为由 Server 集成测试覆盖。
	acp.Agent
}

// TestRegistrySelectsDefaultAndExplicitAdapter 验证省略名称与显式名称会选择同一个注册项。
// 若默认分支绕过注册表、显式分支选择其他工厂或工厂未执行，本测试应失败。
func TestRegistrySelectsDefaultAndExplicitAdapter(t *testing.T) {
	t.Parallel()

	registry, err := NewRegistry("codex")
	if err != nil {
		t.Fatalf("创建注册表失败: %v", err)
	}

	created := 0
	err = registry.Register(Registration{
		Name: "codex",
		Factory: func(context.Context) (acp.Agent, error) {
			created++
			return &registryTestAgent{}, nil
		},
	})
	if err != nil {
		t.Fatalf("注册 Codex Adapter 失败: %v", err)
	}

	for _, requested := range []string{"", "codex"} {
		selection, selectErr := registry.Select(context.Background(), requested)
		if selectErr != nil {
			t.Fatalf("选择 %q 失败: %v", requested, selectErr)
		}
		if selection.Name != "codex" {
			t.Fatalf("选择 %q 得到 %q，期望 codex", requested, selection.Name)
		}
	}

	if created != 2 {
		t.Fatalf("工厂执行 %d 次，期望 2 次", created)
	}
}

// TestRegistryRejectsInvalidRegistration 验证空名称、空工厂和重复名称都不能污染注册表。
// 若无效注册被静默接受或覆盖已有工厂，本测试应失败。
func TestRegistryRejectsInvalidRegistration(t *testing.T) {
	t.Parallel()

	validFactory := func(context.Context) (acp.Agent, error) { return &registryTestAgent{}, nil }
	tests := []struct {
		// name 描述当前无效注册场景。
		name string
		// registrations 是按顺序执行的注册输入。
		registrations []Registration
		// wantErr 是最后一次注册必须保留的稳定根因。
		wantErr error
	}{
		{
			name: "空名称",
			registrations: []Registration{
				{Name: "", Factory: validFactory},
			},
			wantErr: ErrInvalidAdapter,
		},
		{
			name: "空工厂",
			registrations: []Registration{
				{Name: "codex", Factory: nil},
			},
			wantErr: ErrInvalidAdapter,
		},
		{
			name: "重复名称",
			registrations: []Registration{
				{Name: "codex", Factory: validFactory},
				{Name: "codex", Factory: validFactory},
			},
			wantErr: ErrAdapterAlreadyRegistered,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry, err := NewRegistry("codex")
			if err != nil {
				t.Fatalf("创建注册表失败: %v", err)
			}

			for _, registration := range tt.registrations {
				err = registry.Register(registration)
				if err != nil {
					break
				}
			}

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("注册错误为 %v，期望匹配 %v", err, tt.wantErr)
			}
		})
	}
}

// TestRegistryRejectsNilFactoryResult 验证工厂成功返回 nil 时仍会在启动协议前失败。
// 若 nil Agent 被包装进 Selection 并延迟到 Server 才报错，本测试应失败。
func TestRegistryRejectsNilFactoryResult(t *testing.T) {
	t.Parallel()

	registry, err := NewRegistry("codex")
	if err != nil {
		t.Fatalf("创建注册表失败: %v", err)
	}
	err = registry.Register(Registration{
		Name: "codex",
		Factory: func(context.Context) (acp.Agent, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("注册 Codex Adapter 失败: %v", err)
	}

	_, err = registry.Select(context.Background(), "codex")
	if !errors.Is(err, ErrInvalidAdapter) {
		t.Fatalf("选择错误为 %v，期望匹配 %v", err, ErrInvalidAdapter)
	}
}

// TestRegistryRejectsUnknownAdapter 验证未知名称会在执行任何工厂前返回稳定错误。
// 若选择器任意回退到默认 Adapter，本测试应失败。
func TestRegistryRejectsUnknownAdapter(t *testing.T) {
	t.Parallel()

	registry, err := NewRegistry("codex")
	if err != nil {
		t.Fatalf("创建注册表失败: %v", err)
	}
	err = registry.Register(Registration{
		Name: "codex",
		Factory: func(context.Context) (acp.Agent, error) {
			t.Fatal("未知 Adapter 不应执行默认工厂")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("注册 Codex Adapter 失败: %v", err)
	}

	_, err = registry.Select(context.Background(), "unknown")
	if !errors.Is(err, ErrAdapterNotFound) {
		t.Fatalf("选择错误为 %v，期望匹配 %v", err, ErrAdapterNotFound)
	}
}

// TestRegistryWrapsFactoryFailure 验证 Adapter 构造失败保留原始错误链与 Adapter 名称。
// 若构造错误被吞掉或改写为不可检查的字符串，本测试应失败。
func TestRegistryWrapsFactoryFailure(t *testing.T) {
	t.Parallel()

	factoryErr := errors.New("codex unavailable")
	registry, err := NewRegistry("codex")
	if err != nil {
		t.Fatalf("创建注册表失败: %v", err)
	}
	err = registry.Register(Registration{
		Name: "codex",
		Factory: func(context.Context) (acp.Agent, error) {
			return nil, factoryErr
		},
	})
	if err != nil {
		t.Fatalf("注册 Codex Adapter 失败: %v", err)
	}

	_, err = registry.Select(context.Background(), "codex")
	if !errors.Is(err, factoryErr) {
		t.Fatalf("选择错误为 %v，期望保留 %v", err, factoryErr)
	}
}
