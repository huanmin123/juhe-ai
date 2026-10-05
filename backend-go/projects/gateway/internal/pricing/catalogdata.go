// catalogdata.go — 模型目录 JSON 单一数据源（计划-20261005T230500000Z）。
//
// catalogdata/*.json 按供应商承载全部目录事实（rawModel 展开值，go:embed
// 编译期打包）；派生规则仍留 Go（pricing() 构建链）。导出/加载用反射做
// rawModel ↔ JSON 的通用映射：字段名映射为首字母小写的 camelCase，
// *float64/*int 为 null 或数字，[]string / map[string][]string 原样——
// rawModel 增删字段时无需改本文件（缺字段零值、多字段忽略）。
//
// 数据维护方式：改 JSON → go build。行注释中的裁决上下文沉淀在各 JSON 的
// "notes" 字段与 docs/functions/厂商模型目录更新与清洗指南.md；纯过程性
// 注释留在 git 历史。
package pricing

import (
	"embed"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

//go:embed catalogdata/*.json
var catalogDataFS embed.FS

// loadProviderCatalogModels 读指定供应商的 JSON 目录并还原为 rawModel 切片，
// 行序按 model 字典序稳定化（JSON 手工编辑不因行序引发排序门禁噪音）。
func loadProviderCatalogModels(provider string) ([]rawModel, error) {
	raw, err := catalogDataFS.ReadFile("catalogdata/" + provider + ".json")
	if err != nil {
		return nil, fmt.Errorf("read catalogdata/%s.json: %w", provider, err)
	}
	var doc struct {
		Provider string            `json:"provider"`
		Models   []map[string]any  `json:"models"`
		Notes    map[string]string `json:"notes,omitempty"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse catalogdata/%s.json: %w", provider, err)
	}
	if doc.Provider != provider {
		return nil, fmt.Errorf("catalogdata/%s.json provider = %q", provider, doc.Provider)
	}
	models := make([]rawModel, 0, len(doc.Models))
	for index, entry := range doc.Models {
		model, err := jsonToRawModel(entry)
		if err != nil {
			return nil, fmt.Errorf("catalogdata/%s.json models[%d]: %w", provider, index, err)
		}
		models = append(models, model)
	}
	sort.SliceStable(models, func(left, right int) bool { return models[left].Model < models[right].Model })
	return models, nil
}

// mustLoadProviderCatalogModels 是数据 var 的包级初始化入口（九家快照
// data_*.go 统一改为 JSON 加载）：embed 读取或校验失败在启动期 panic——
// 目录数据是编译期事实，坏数据不应静默降级为空目录。
func mustLoadProviderCatalogModels(provider string) []rawModel {
	models, err := loadProviderCatalogModels(provider)
	if err != nil {
		panic(err)
	}
	return models
}

// jsonToRawModel 反射映射：JSON 键（首字母小写 camelCase）→ rawModel 导出
// 字段。支持的 Kind：string、bool、*float64、*int、[]string、
// map[string][]string；未知键忽略（向前兼容），类型不匹配报错。
func jsonToRawModel(entry map[string]any) (rawModel, error) {
	var out rawModel
	value := reflect.ValueOf(&out).Elem()
	typ := value.Type()
	for key, raw := range entry {
		if key == "notes" {
			continue
		}
		field := value.FieldByName(strings.ToUpper(key[:1]) + key[1:])
		if !field.IsValid() {
			continue
		}
		if raw == nil {
			continue
		}
		switch field.Kind() {
		case reflect.String:
			text, ok := raw.(string)
			if !ok {
				return out, fmt.Errorf("%s: want string, got %T", key, raw)
			}
			field.SetString(text)
		case reflect.Bool:
			flag, ok := raw.(bool)
			if !ok {
				return out, fmt.Errorf("%s: want bool, got %T", key, raw)
			}
			field.SetBool(flag)
		case reflect.Ptr:
			// *float64 / *int / *bool 可选字段（rawModel 无二级指针与 *string）。
			switch elem := field.Type().Elem().Kind(); elem {
			case reflect.Float64:
				number, ok := raw.(float64)
				if !ok {
					return out, fmt.Errorf("%s: want number, got %T", key, raw)
				}
				value := number
				field.Set(reflect.ValueOf(&value))
			case reflect.Int:
				number, ok := raw.(float64)
				if !ok {
					return out, fmt.Errorf("%s: want number, got %T", key, raw)
				}
				value := int(number)
				field.Set(reflect.ValueOf(&value))
			case reflect.Bool:
				flag, ok := raw.(bool)
				if !ok {
					return out, fmt.Errorf("%s: want bool, got %T", key, raw)
				}
				field.Set(reflect.ValueOf(&flag))
			default:
				return out, fmt.Errorf("%s: unsupported pointer elem kind %s", key, elem)
			}
		case reflect.Slice:
			list, ok := raw.([]any)
			if !ok {
				return out, fmt.Errorf("%s: want array, got %T", key, raw)
			}
			items := make([]string, 0, len(list))
			for _, element := range list {
				text, ok := element.(string)
				if !ok {
					return out, fmt.Errorf("%s: want string elements, got %T", key, element)
				}
				items = append(items, text)
			}
			if field.Type().Elem().Kind() == reflect.String {
				field.Set(reflect.ValueOf(items))
			}
		case reflect.Map:
			nested, ok := raw.(map[string]any)
			if !ok {
				return out, fmt.Errorf("%s: want object, got %T", key, raw)
			}
			matrix := map[string][]string{}
			for protocol, toolsRaw := range nested {
				tools, ok := toolsRaw.([]any)
				if !ok {
					return out, fmt.Errorf("%s.%s: want array, got %T", key, protocol, toolsRaw)
				}
				names := make([]string, 0, len(tools))
				for _, tool := range tools {
					text, ok := tool.(string)
					if !ok {
						return out, fmt.Errorf("%s.%s: want string elements, got %T", key, protocol, tool)
					}
					names = append(names, text)
				}
				matrix[protocol] = names
			}
			field.Set(reflect.ValueOf(matrix))
		default:
			return out, fmt.Errorf("%s: unsupported field kind %s", key, field.Kind())
		}
	}
	_ = typ
	return out, nil
}

// rawModelToJSON 是 jsonToRawModel 的逆映射（导出用，见
// TestExportCatalogData）：字段名转首字母小写，nil 指针省略，空切片保留
// 为 [] 以区分「显式空」与「未声明」。
func rawModelToJSON(m rawModel) map[string]any {
	out := map[string]any{}
	value := reflect.ValueOf(m)
	typ := value.Type()
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		key := strings.ToLower(field.Name[:1]) + field.Name[1:]
		fieldValue := value.Field(index)
		switch fieldValue.Kind() {
		case reflect.String:
			out[key] = fieldValue.String()
		case reflect.Bool:
			out[key] = fieldValue.Bool()
		case reflect.Ptr:
			if fieldValue.IsNil() {
				continue
			}
			out[key] = fieldValue.Elem().Interface()
		case reflect.Slice:
			if fieldValue.IsNil() {
				continue
			}
			items := make([]any, 0, fieldValue.Len())
			for i := 0; i < fieldValue.Len(); i++ {
				items = append(items, fieldValue.Index(i).Interface())
			}
			out[key] = items
		case reflect.Map:
			if fieldValue.IsNil() {
				continue
			}
			nested := map[string]any{}
			for _, protocol := range fieldValue.MapKeys() {
				nested[protocol.String()] = fieldValue.MapIndex(protocol).Interface()
			}
			out[key] = nested
		default:
			panic(fmt.Sprintf("rawModelToJSON: unsupported field %s kind %s", field.Name, fieldValue.Kind()))
		}
	}
	return out
}
