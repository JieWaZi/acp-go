package acp

import "encoding/json"

// MarshalJSON 保留工具更新中显式清空的集合，省略字段才表示不修改当前集合。
// 此扩展不改生成类型；上游 omitempty 会把 [] 与 nil 混同，导致标准清空语义在内嵌传输中丢失。
func (update SessionToolCallUpdate) MarshalJSON() ([]byte, error) {
	type plainUpdate SessionToolCallUpdate
	var content *[]ToolCallContent
	if update.Content != nil {
		content = &update.Content
	}
	var locations *[]ToolCallLocation
	if update.Locations != nil {
		locations = &update.Locations
	}
	return json.Marshal(struct {
		// plainUpdate 保留其他标准字段的原有编码方式。
		*plainUpdate
		// Content 区分缺省与显式空的完整内容集合。
		Content *[]ToolCallContent `json:"content,omitempty"`
		// Locations 区分缺省与显式空的完整位置集合。
		Locations *[]ToolCallLocation `json:"locations,omitempty"`
	}{plainUpdate: (*plainUpdate)(&update), Content: content, Locations: locations})
}
