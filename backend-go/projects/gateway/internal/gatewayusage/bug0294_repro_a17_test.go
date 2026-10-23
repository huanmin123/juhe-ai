// BUG-0294 / A17 指针容器循环引用回归（由最小复现定性载体转正）。
//
// 档案：docs/bug/问题-0294-迁移期行为存疑按现状断言未建档清点.md A17。
// 原注释：wj_usage_pure_test.go:114（「OrderedObject 自引用会使 identitySet 失效
// （只按 slice/map 指针跟踪容器），visitJSONLikeValue 对 *OrderedObject 无限递归
// 直至栈溢出；Node WeakSet 对普通对象生效，不会栈溢出」）。
//
// 复核结论：注释过期（描述的旧行为已不存在，已被 w14g 批次修复），本批回正注释。
// 证据：
//   - size.go identitySet.add 现登记 reflect.Slice / reflect.Map /
//     reflect.Ptr 三类容器指针（行内注释自述 w14g 修复指针容器循环引用保护失效）；
//   - 本回归把自引用 *OrderedObject 输入 EstimateJSONLikeBytes，实现有限
//     终止且给出确定估算值（外层 {} 2 + 键 "self" 4+3 + 循环容器 16 + 逗号 1
//     = 26 字节），无栈溢出；
//   - jobs/internal/usagewriter/size.go 的同族实现已同步同一修复。
//
// 红形态说明：若该修复回退，本测试将以测试二进制栈溢出崩溃的方式变红
// （Go 栈溢出不可 recover），因此不写「断言会失败」的红测试。
package gatewayusage

import "testing"

// TestBug0294A17OrderedObjectSelfReferenceTerminates 回归指针容器循环引用：
// identitySet 按 Ptr 指针截断，估算有限终止。
func TestBug0294A17OrderedObjectSelfReferenceTerminates(t *testing.T) {
	obj := NewOrderedObject()
	obj.Set("self", obj)

	got := EstimateJSONLikeBytes(obj, EstimateJSONLikeBytesOptions{})
	if got != 26 {
		t.Fatalf("自引用 OrderedObject 估算 = %d，期望 26（有限终止）", got)
	}

	// 经 map 间接引用同一指针容器同样必须有限终止（同一 identitySet 实例内去重）。
	wrapper := map[string]any{"inner": obj}
	if indirect := EstimateJSONLikeBytes(wrapper, EstimateJSONLikeBytesOptions{}); indirect == 0 {
		t.Fatal("经 map 间接自引用必须有限终止，实际估算为 0")
	}
}
