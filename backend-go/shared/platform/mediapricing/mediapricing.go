// Package mediapricing 定义非对话媒体统一计费模板
// （docs/functions/非对话媒体统一计费设计.md §4、§5）。
//
// 该包是 gateway 运行时目录、maintenance seed 与两套计费引擎共用的
// 唯一结构定义：models.media_pricing_json 的严格解析、分辨率档位归桶、
// 档位倍率与单位价格计算都在这里实现，避免跨 module 各写一套。
package mediapricing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Unit 是统一媒体计费单位。
type Unit string

const (
	UnitSecond Unit = "second"
	UnitImage  Unit = "image"
	UnitCall   Unit = "call"
)

// Kind 是媒体类型，用于区分同一个 unit 下的档位规则：
//   - 视频 second 必须带完整五档倍率；
//   - 音频 second 不带档位；
//   - 图片 image 必须带完整三档倍率；
//   - 视频 call 不带档位。
type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
	KindImage Kind = "image"
)

const (
	// Version 是当前唯一支持的 mediaPricing 结构版本。
	Version = 1
	// MaxJSONBytes 是 media_pricing_json 的体积上限（设计 §5.2）。
	MaxJSONBytes = 4096
	// MaxBaseUsd 是 baseUsd 的硬上限，防止误输入。
	MaxBaseUsd = 1000
	// MaxTierMultiplier 是档位倍率硬上限。
	MaxTierMultiplier = 100
)

// 视频档位按短边归桶（设计 §4.1）。
const (
	VideoTier480P  = "480p"
	VideoTier720P  = "720p"
	VideoTier1080P = "1080p"
	VideoTier1440P = "1440p"
	VideoTier2160P = "2160p"
)

// 图片档位按长边归桶（设计 §4.2）。
const (
	ImageTier1K = "1k"
	ImageTier2K = "2k"
	ImageTier4K = "4k"
)

// VideoTiers 是视频完整档位集合，顺序固定用于校验与展示。
var VideoTiers = []string{VideoTier480P, VideoTier720P, VideoTier1080P, VideoTier1440P, VideoTier2160P}

// ImageTiers 是图片完整档位集合，顺序固定用于校验与展示。
var ImageTiers = []string{ImageTier1K, ImageTier2K, ImageTier4K}

// DefaultVideoTierMultipliers 是 720p 为 1.0 锚点的默认视频倍率。
var DefaultVideoTierMultipliers = map[string]float64{
	VideoTier480P:  0.5,
	VideoTier720P:  1.0,
	VideoTier1080P: 2.0,
	VideoTier1440P: 3.0,
	VideoTier2160P: 5.0,
}

// DefaultImageTierMultipliers 是 1k 为 1.0 锚点的默认图片倍率。
var DefaultImageTierMultipliers = map[string]float64{
	ImageTier1K: 1.0,
	ImageTier2K: 1.6,
	ImageTier4K: 3.0,
}

var (
	// ErrEmpty 表示 media_pricing_json 为空（NULL/空串按 legacy 处理，
	// 不属于非法价格）。
	ErrEmpty = errors.New("media pricing is empty")
	// ErrInvalid 是结构或取值校验失败的基类，调用方用 errors.Is 判断。
	ErrInvalid = errors.New("invalid media pricing")
)

// Pricing 是 media_pricing_json 的严格结构。
//
// BaseUsd 使用值类型而不是指针：0 是有效免费价；价格是否存在由
// 上层独立的 HasMediaPricing 标志或 *Pricing 是否为 nil 表达，
// 不得用 BaseUsd <= 0 判断未定价（设计 §5.2）。
type Pricing struct {
	Version         int                `json:"version"`
	Unit            Unit               `json:"unit"`
	BaseUsd         float64            `json:"baseUsd"`
	BaseTier        string             `json:"baseTier"`
	TierMultipliers map[string]float64 `json:"tierMultipliers"`
}

// pricingWire 用指针区分「字段缺失」与「合法零值」。
type pricingWire struct {
	Version         *int               `json:"version"`
	Unit            *string            `json:"unit"`
	BaseUsd         *float64           `json:"baseUsd"`
	BaseTier        *string            `json:"baseTier"`
	TierMultipliers map[string]float64 `json:"tierMultipliers"`
}

// Parse 严格解析 media_pricing_json。空输入返回 ErrEmpty，未知字段、
// 尾随内容、非法结构一律返回错误。调用方若把空值视作 legacy，使用
// ParseOptional。
func Parse(data []byte) (*Pricing, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, ErrEmpty
	}
	if len(trimmed) > MaxJSONBytes {
		return nil, fmt.Errorf("%w: json exceeds %d bytes", ErrInvalid, MaxJSONBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var wire pricingWire
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, err
	}
	if wire.Version == nil {
		return nil, fmt.Errorf("%w: missing version", ErrInvalid)
	}
	if wire.Unit == nil {
		return nil, fmt.Errorf("%w: missing unit", ErrInvalid)
	}
	if wire.BaseUsd == nil {
		return nil, fmt.Errorf("%w: missing baseUsd", ErrInvalid)
	}

	p := &Pricing{
		Version:         *wire.Version,
		Unit:            Unit(strings.TrimSpace(*wire.Unit)),
		BaseUsd:         *wire.BaseUsd,
		TierMultipliers: map[string]float64{},
	}
	if wire.BaseTier != nil {
		p.BaseTier = strings.TrimSpace(*wire.BaseTier)
	}
	for tier, multiplier := range wire.TierMultipliers {
		p.TierMultipliers[tier] = multiplier
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// ParseOptional 是 Parse 的 legacy 友好版本：空输入或 null 返回 nil, nil。
func ParseOptional(data []byte) (*Pricing, error) {
	p, err := Parse(data)
	if errors.Is(err, ErrEmpty) {
		return nil, nil
	}
	return p, err
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: trailing json value", ErrInvalid)
		}
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// Validate 校验结构本身。它允许 unit=second 的两种合法形态：
// 音频（无档位）与视频（完整五档）；具体媒体类型由 ValidateFor 收紧。
func (p *Pricing) Validate() error {
	if p == nil {
		return fmt.Errorf("%w: nil pricing", ErrInvalid)
	}
	if p.Version != Version {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalid, p.Version)
	}
	if !p.Unit.Valid() {
		return fmt.Errorf("%w: unknown unit %q", ErrInvalid, p.Unit)
	}
	if math.IsNaN(p.BaseUsd) || math.IsInf(p.BaseUsd, 0) || p.BaseUsd < 0 || p.BaseUsd > MaxBaseUsd {
		return fmt.Errorf("%w: baseUsd must be finite in [0,%d]", ErrInvalid, MaxBaseUsd)
	}
	if p.TierMultipliers == nil {
		p.TierMultipliers = map[string]float64{}
	}

	switch p.Unit {
	case UnitCall:
		if p.BaseTier != "" || len(p.TierMultipliers) != 0 {
			return fmt.Errorf("%w: unit=call requires empty baseTier and tierMultipliers", ErrInvalid)
		}
		return nil
	case UnitImage:
		return p.validateTierSet(ImageTiers, "image")
	case UnitSecond:
		if len(p.TierMultipliers) == 0 {
			if p.BaseTier != "" {
				return fmt.Errorf("%w: baseTier requires tierMultipliers", ErrInvalid)
			}
			return nil
		}
		return p.validateTierSet(VideoTiers, "video")
	default:
		return fmt.Errorf("%w: unknown unit %q", ErrInvalid, p.Unit)
	}
}

func (p *Pricing) validateTierSet(expected []string, kind string) error {
	if len(p.TierMultipliers) != len(expected) {
		return fmt.Errorf("%w: %s requires exactly %d tiers", ErrInvalid, kind, len(expected))
	}
	for _, tier := range expected {
		multiplier, ok := p.TierMultipliers[tier]
		if !ok {
			return fmt.Errorf("%w: missing tier %q", ErrInvalid, tier)
		}
		if math.IsNaN(multiplier) || math.IsInf(multiplier, 0) || multiplier <= 0 || multiplier > MaxTierMultiplier {
			return fmt.Errorf("%w: tier %q multiplier must be finite in (0,%d]", ErrInvalid, tier, MaxTierMultiplier)
		}
	}
	for tier := range p.TierMultipliers {
		if !contains(expected, tier) {
			return fmt.Errorf("%w: unknown tier %q", ErrInvalid, tier)
		}
	}
	if p.BaseTier == "" {
		return fmt.Errorf("%w: baseTier is required", ErrInvalid)
	}
	if !contains(expected, p.BaseTier) {
		return fmt.Errorf("%w: baseTier %q is not in tier set", ErrInvalid, p.BaseTier)
	}
	if math.Abs(p.TierMultipliers[p.BaseTier]-1.0) > 1e-9 {
		return fmt.Errorf("%w: baseTier %q multiplier must be 1.0", ErrInvalid, p.BaseTier)
	}
	return nil
}

// ValidateFor 在结构校验之外按媒体类型收紧 unit 与档位组合。
func (p *Pricing) ValidateFor(kind Kind) error {
	if err := p.Validate(); err != nil {
		return err
	}
	switch kind {
	case KindVideo:
		if p.Unit == UnitCall {
			return nil
		}
		if p.Unit != UnitSecond {
			return fmt.Errorf("%w: video requires unit=second or call", ErrInvalid)
		}
		if len(p.TierMultipliers) == 0 {
			return fmt.Errorf("%w: video second requires the full tier set", ErrInvalid)
		}
		return nil
	case KindAudio:
		if p.Unit != UnitSecond {
			return fmt.Errorf("%w: audio requires unit=second", ErrInvalid)
		}
		if len(p.TierMultipliers) != 0 || p.BaseTier != "" {
			return fmt.Errorf("%w: audio second cannot carry tiers", ErrInvalid)
		}
		return nil
	case KindImage:
		if p.Unit != UnitImage {
			return fmt.Errorf("%w: image requires unit=image", ErrInvalid)
		}
		return nil
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, kind)
	}
}

// ValidateForMode 按目录 mode 校验（mode 为模型目录的原始值）。
func (p *Pricing) ValidateForMode(mode string) error {
	kind, ok := KindForMode(mode)
	if !ok {
		return fmt.Errorf("%w: mode %q does not support mediaPricing", ErrInvalid, mode)
	}
	return p.ValidateFor(kind)
}

// Valid 报告 unit 是否是已知枚举值。
func (u Unit) Valid() bool {
	switch u {
	case UnitSecond, UnitImage, UnitCall:
		return true
	default:
		return false
	}
}

// KindForMode 把模型目录 mode 映射为媒体类型。
func KindForMode(mode string) (Kind, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "video":
		return KindVideo, true
	case "image", "image_generation":
		return KindImage, true
	case "audio":
		return KindAudio, true
	default:
		return "", false
	}
}

// DefaultUnitForKind 返回新增媒体模型的默认计费单位（设计 §5.3）。
func DefaultUnitForKind(kind Kind) Unit {
	switch kind {
	case KindImage:
		return UnitImage
	case KindVideo, KindAudio:
		return UnitSecond
	default:
		return ""
	}
}

// DefaultBaseTier 返回该单位默认基准档；call/audio second 为空。
func DefaultBaseTier(unit Unit) string {
	switch unit {
	case UnitImage:
		return ImageTier1K
	case UnitSecond:
		return VideoTier720P
	default:
		return ""
	}
}

// DefaultTierMultipliers 返回以 baseTier 倍率 1.0 为锚点、按设计 §4.3
// 等比缩放后的默认倍率表。baseTier 为空时使用该单位锚点档。
func DefaultTierMultipliers(unit Unit, baseTier string) (map[string]float64, error) {
	var defaults map[string]float64
	var anchor string
	switch unit {
	case UnitImage:
		defaults, anchor = DefaultImageTierMultipliers, ImageTier1K
	case UnitSecond:
		defaults, anchor = DefaultVideoTierMultipliers, VideoTier720P
	case UnitCall:
		return map[string]float64{}, nil
	default:
		return nil, fmt.Errorf("%w: unknown unit %q", ErrInvalid, unit)
	}
	if baseTier == "" {
		baseTier = anchor
	}
	baseMultiplier, ok := defaults[baseTier]
	if !ok {
		return nil, fmt.Errorf("%w: unknown baseTier %q", ErrInvalid, baseTier)
	}
	scale := 1 / baseMultiplier
	out := make(map[string]float64, len(defaults))
	for tier, multiplier := range defaults {
		out[tier] = multiplier * scale
	}
	return out, nil
}

// VideoTierForSize 按短边归桶；宽高任一非正返回 false。
func VideoTierForSize(width, height int) (string, bool) {
	if width <= 0 || height <= 0 {
		return "", false
	}
	short := width
	if height < short {
		short = height
	}
	switch {
	case short <= 576:
		return VideoTier480P, true
	case short <= 900:
		return VideoTier720P, true
	case short <= 1350:
		return VideoTier1080P, true
	case short <= 1800:
		return VideoTier1440P, true
	default:
		return VideoTier2160P, true
	}
}

// ImageTierForSize 按长边归桶；宽高任一非正返回 false。
func ImageTierForSize(width, height int) (string, bool) {
	if width <= 0 || height <= 0 {
		return "", false
	}
	long := width
	if height > long {
		long = height
	}
	switch {
	case long <= 1280:
		return ImageTier1K, true
	case long <= 2560:
		return ImageTier2K, true
	default:
		return ImageTier4K, true
	}
}

// ParseSize 解析 "WxH" 像素串（x/X 与 * 分隔均接受），宽高必须为正。
// 请求 size 与任务创建参数共用同一解析口径，避免各链路大小写/分隔符分叉。
func ParseSize(value string) (width, height int, ok bool) {
	value = strings.TrimSpace(value)
	index := strings.IndexAny(value, "xX*")
	if index <= 0 || index >= len(value)-1 {
		return 0, 0, false
	}
	width, widthErr := strconv.Atoi(strings.TrimSpace(value[:index]))
	height, heightErr := strconv.Atoi(strings.TrimSpace(value[index+1:]))
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// TierForDimensions 按媒体类型选择档位。
func TierForDimensions(kind Kind, width, height int) (string, bool) {
	switch kind {
	case KindVideo:
		return VideoTierForSize(width, height)
	case KindImage:
		return ImageTierForSize(width, height)
	default:
		return "", false
	}
}

// PriceForTier 返回实际单价、倍率与生效档位。
//
// tier 为空时使用 BaseTier；音频与按次要求 tier 为空且倍率恒 1.0。
// tier 非空但不在倍率表中返回错误，不回落 BaseTier 猜测。
func (p *Pricing) PriceForTier(tier string) (unitPriceUsd float64, multiplier float64, effectiveTier string, err error) {
	if err := p.Validate(); err != nil {
		return 0, 0, "", err
	}
	tier = strings.TrimSpace(tier)
	if p.Unit == UnitCall || (p.Unit == UnitSecond && len(p.TierMultipliers) == 0) {
		if tier != "" {
			return 0, 0, "", fmt.Errorf("%w: unit=%s cannot carry tier %q", ErrInvalid, p.Unit, tier)
		}
		return p.BaseUsd, 1.0, "", nil
	}
	if tier == "" {
		tier = p.BaseTier
	}
	if tier == "" {
		return 0, 0, "", fmt.Errorf("%w: baseTier is empty", ErrInvalid)
	}
	multiplier, ok := p.TierMultipliers[tier]
	if !ok {
		return 0, 0, "", fmt.Errorf("%w: tier %q is not configured", ErrInvalid, tier)
	}
	return p.BaseUsd * multiplier, multiplier, tier, nil
}

// Clone 返回深拷贝，避免调用方共享 TierMultipliers map。
func (p *Pricing) Clone() *Pricing {
	if p == nil {
		return nil
	}
	out := *p
	out.TierMultipliers = make(map[string]float64, len(p.TierMultipliers))
	for tier, multiplier := range p.TierMultipliers {
		out.TierMultipliers[tier] = multiplier
	}
	return &out
}

// CanonicalJSON 返回紧凑 JSON（空档位表序列化为 {}，不是 null）。
func (p *Pricing) CanonicalJSON() ([]byte, error) {
	if p == nil {
		return nil, ErrEmpty
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	clone := p.Clone()
	if clone.TierMultipliers == nil {
		clone.TierMultipliers = map[string]float64{}
	}
	data, err := json.Marshal(clone)
	if err != nil {
		return nil, fmt.Errorf("marshal media pricing: %w", err)
	}
	if len(data) > MaxJSONBytes {
		return nil, fmt.Errorf("%w: json exceeds %d bytes", ErrInvalid, MaxJSONBytes)
	}
	return data, nil
}

// CanonicalString 返回规范 JSON 字符串，便于写入 DB 与 seed。
func (p *Pricing) CanonicalString() (string, error) {
	data, err := p.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
