package protocol

import (
	"bytes"
	"encoding/json"
)

// OptionalNullable 表示 JSON 字段的 absent、null、value 三种独立状态。
type OptionalNullable[T any] struct {
	// present 记录字段是否出现在输入对象中。
	present bool
	// null 记录已出现字段是否携带显式 JSON null。
	null bool
	// value 保存非 null 状态下的强类型值。
	value T
}

// NewOptionalNull 创建一个显式 JSON null 状态。
func NewOptionalNull[T any]() OptionalNullable[T] {
	return OptionalNullable[T]{present: true, null: true}
}

// NewOptionalValue 创建一个包含强类型值的已出现状态。
func NewOptionalValue[T any](value T) OptionalNullable[T] {
	return OptionalNullable[T]{present: true, value: value}
}

// IsPresent 返回字段是否出现在 JSON 对象中。
func (o OptionalNullable[T]) IsPresent() bool {
	return o.present
}

// IsNull 返回字段是否以显式 JSON null 出现。
func (o OptionalNullable[T]) IsNull() bool {
	return o.present && o.null
}

// Value 返回非 null 值；缺省或显式 null 状态返回 false。
func (o OptionalNullable[T]) Value() (T, bool) {
	return o.value, o.present && !o.null
}

// IsZero 让 encoding/json 的 omitzero 在字段缺省时省略整个键。
func (o OptionalNullable[T]) IsZero() bool {
	return !o.present
}

// MarshalJSON 将显式 null 与强类型值写回 wire；缺省状态由父字段的 omitzero 处理。
func (o OptionalNullable[T]) MarshalJSON() ([]byte, error) {
	if !o.present || o.null {
		return []byte("null"), nil
	}
	return json.Marshal(o.value)
}

// UnmarshalJSON 记录字段出现事实，并分别解析 null 或强类型值。
func (o *OptionalNullable[T]) UnmarshalJSON(data []byte) error {
	o.present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		var zero T
		o.null = true
		o.value = zero
		return nil
	}

	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.null = false
	o.value = value
	return nil
}
