package mediapricing

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check func(t *testing.T, p *Pricing)
	}{
		{
			name:  "audio second",
			input: `{"version":1,"unit":"second","baseUsd":0.0002,"baseTier":"","tierMultipliers":{}}`,
			check: func(t *testing.T, p *Pricing) {
				if p.Unit != UnitSecond || len(p.TierMultipliers) != 0 {
					t.Fatalf("unexpected audio pricing: %+v", p)
				}
			},
		},
		{
			name:  "image tiers",
			input: `{"version":1,"unit":"image","baseUsd":0.04,"baseTier":"1k","tierMultipliers":{"1k":1.0,"2k":1.6,"4k":3.0}}`,
			check: func(t *testing.T, p *Pricing) {
				price, multiplier, tier, err := p.PriceForTier("2k")
				if err != nil || tier != "2k" || multiplier != 1.6 || math.Abs(price-0.064) > 1e-12 {
					t.Fatalf("price for 2k = %v/%v/%q/%v", price, multiplier, tier, err)
				}
			},
		},
		{
			name:  "video call",
			input: `{"version":1,"unit":"call","baseUsd":0,"baseTier":"","tierMultipliers":{}}`,
			check: func(t *testing.T, p *Pricing) {
				price, multiplier, tier, err := p.PriceForTier("")
				if err != nil || price != 0 || multiplier != 1 || tier != "" {
					t.Fatalf("free call price = %v/%v/%q/%v", price, multiplier, tier, err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse([]byte(tc.input))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			tc.check(t, p)
		})
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"missing version":        `{"unit":"second","baseUsd":0.1}`,
		"missing unit":           `{"version":1,"baseUsd":0.1}`,
		"missing baseUsd":        `{"version":1,"unit":"second"}`,
		"unknown version":        `{"version":2,"unit":"second","baseUsd":0.1}`,
		"unknown unit":           `{"version":1,"unit":"minute","baseUsd":0.1}`,
		"unknown field":          `{"version":1,"unit":"second","baseUsd":0.1,"extra":true}`,
		"trailing value":         `{"version":1,"unit":"second","baseUsd":0.1} {}`,
		"negative base":          `{"version":1,"unit":"call","baseUsd":-0.1}`,
		"base over cap":          `{"version":1,"unit":"call","baseUsd":1000.01}`,
		"call with tier":         `{"version":1,"unit":"call","baseUsd":0.1,"baseTier":"720p"}`,
		"image missing tiers":    `{"version":1,"unit":"image","baseUsd":0.1}`,
		"image partial":          `{"version":1,"unit":"image","baseUsd":0.1,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6}}`,
		"video partial":          `{"version":1,"unit":"second","baseUsd":0.1,"baseTier":"720p","tierMultipliers":{"720p":1,"1080p":2}}`,
		"unknown tier":           `{"version":1,"unit":"image","baseUsd":0.1,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6,"4k":3,"8k":4}}`,
		"base tier missing":      `{"version":1,"unit":"image","baseUsd":0.1,"baseTier":"9k","tierMultipliers":{"1k":1,"2k":1.6,"4k":3}}`,
		"base multiplier not 1":  `{"version":1,"unit":"image","baseUsd":0.1,"baseTier":"1k","tierMultipliers":{"1k":1.1,"2k":1.6,"4k":3}}`,
		"zero multiplier":        `{"version":1,"unit":"image","baseUsd":0.1,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":0,"4k":3}}`,
		"multiplier over cap":    `{"version":1,"unit":"image","baseUsd":0.1,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6,"4k":101}}`,
		"baseTier without tiers": `{"version":1,"unit":"second","baseUsd":0.1,"baseTier":"720p"}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(input)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse(%s) err = %v, want ErrInvalid", input, err)
			}
		})
	}
}

func TestParseOptional(t *testing.T) {
	for _, input := range []string{"", "  ", "null"} {
		p, err := ParseOptional([]byte(input))
		if err != nil || p != nil {
			t.Fatalf("ParseOptional(%q) = %+v, %v", input, p, err)
		}
	}
}

func TestParseEmpty(t *testing.T) {
	for _, input := range []string{"", "  ", "null"} {
		if _, err := Parse([]byte(input)); !errors.Is(err, ErrEmpty) {
			t.Fatalf("Parse(%q) err = %v, want ErrEmpty", input, err)
		}
	}
}

func TestParseSizeLimit(t *testing.T) {
	input := `{"version":1,"unit":"call","baseUsd":0.1,"extra":"` + strings.Repeat("a", MaxJSONBytes) + `"}`
	if _, err := Parse([]byte(input)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize err = %v", err)
	}
}

func TestValidateFor(t *testing.T) {
	audio, err := Parse([]byte(`{"version":1,"unit":"second","baseUsd":0.1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := audio.ValidateFor(KindAudio); err != nil {
		t.Fatalf("audio: %v", err)
	}
	if err := audio.ValidateFor(KindVideo); err == nil {
		t.Fatal("audio pricing accepted as video")
	}
	if err := audio.ValidateFor(KindImage); err == nil {
		t.Fatal("audio pricing accepted as image")
	}

	image, err := Parse([]byte(`{"version":1,"unit":"image","baseUsd":0.1,"baseTier":"1k","tierMultipliers":{"1k":1,"2k":1.6,"4k":3}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := image.ValidateFor(KindImage); err != nil {
		t.Fatalf("image: %v", err)
	}
	if err := image.ValidateFor(KindAudio); err == nil {
		t.Fatal("image pricing accepted as audio")
	}

	video, err := Parse([]byte(`{"version":1,"unit":"second","baseUsd":0.1,"baseTier":"1080p","tierMultipliers":{"480p":0.25,"720p":0.5,"1080p":1,"1440p":1.5,"2160p":2.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := video.ValidateFor(KindVideo); err != nil {
		t.Fatalf("video: %v", err)
	}
	if err := video.ValidateForMode("chat"); err == nil {
		t.Fatal("chat mode accepted")
	}
	if err := video.ValidateForMode("video"); err != nil {
		t.Fatalf("mode video: %v", err)
	}
}

func TestPriceForTier(t *testing.T) {
	video, err := Parse([]byte(`{"version":1,"unit":"second","baseUsd":0.1,"baseTier":"720p","tierMultipliers":{"480p":0.5,"720p":1,"1080p":2,"1440p":3,"2160p":5}}`))
	if err != nil {
		t.Fatal(err)
	}
	price, multiplier, tier, err := video.PriceForTier("")
	if err != nil || tier != "720p" || multiplier != 1 || price != 0.1 {
		t.Fatalf("default tier = %v/%v/%q/%v", price, multiplier, tier, err)
	}
	price, multiplier, tier, err = video.PriceForTier("1080p")
	if err != nil || tier != "1080p" || multiplier != 2 || math.Abs(price-0.2) > 1e-12 {
		t.Fatalf("1080p = %v/%v/%q/%v", price, multiplier, tier, err)
	}
	if _, _, _, err := video.PriceForTier("4k"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown tier err = %v", err)
	}
	if _, _, _, err := video.PriceForTier(""); err != nil {
		t.Fatalf("video default tier err = %v", err)
	}

	audio, _ := Parse([]byte(`{"version":1,"unit":"second","baseUsd":0.2}`))
	price, multiplier, tier, err = audio.PriceForTier("")
	if err != nil || price != 0.2 || multiplier != 1 || tier != "" {
		t.Fatalf("audio = %v/%v/%q/%v", price, multiplier, tier, err)
	}
	if _, _, _, err := audio.PriceForTier("720p"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("audio with tier err = %v", err)
	}
}

func TestDefaultTierMultipliers(t *testing.T) {
	video, err := DefaultTierMultipliers(UnitSecond, "1080p")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"480p": 0.25, "720p": 0.5, "1080p": 1, "1440p": 1.5, "2160p": 2.5}
	for tier, expected := range want {
		if video[tier] != expected {
			t.Fatalf("video[%s] = %v, want %v", tier, video[tier], expected)
		}
	}
	image, err := DefaultTierMultipliers(UnitImage, "2k")
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(image["1k"]-0.625) > 1e-12 || image["2k"] != 1 || math.Abs(image["4k"]-1.875) > 1e-12 {
		t.Fatalf("image scaled multipliers = %+v", image)
	}
	if _, err := DefaultTierMultipliers(UnitCall, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultTierMultipliers(UnitSecond, "4k"); err == nil {
		t.Fatal("invalid base tier accepted")
	}
}

func TestTierForDimensions(t *testing.T) {
	videoCases := []struct {
		width, height int
		want          string
	}{
		{854, 480, "480p"},
		{480, 854, "480p"},
		{1280, 720, "720p"},
		{720, 1280, "720p"},
		{1920, 1080, "1080p"},
		{1080, 1920, "1080p"},
		{2560, 1440, "1440p"},
		{3840, 2160, "2160p"},
		{4096, 2160, "2160p"},
	}
	for _, tc := range videoCases {
		got, ok := VideoTierForSize(tc.width, tc.height)
		if !ok || got != tc.want {
			t.Fatalf("VideoTierForSize(%d,%d) = %q,%v want %q", tc.width, tc.height, got, ok, tc.want)
		}
	}
	imageCases := []struct {
		width, height int
		want          string
	}{
		{1024, 1024, "1k"},
		{1280, 720, "1k"},
		{1536, 1024, "2k"},
		{1024, 1536, "2k"},
		{2048, 2048, "2k"},
		{2560, 1440, "2k"},
		{4096, 4096, "4k"},
	}
	for _, tc := range imageCases {
		got, ok := ImageTierForSize(tc.width, tc.height)
		if !ok || got != tc.want {
			t.Fatalf("ImageTierForSize(%d,%d) = %q,%v want %q", tc.width, tc.height, got, ok, tc.want)
		}
	}
	if _, ok := VideoTierForSize(0, 720); ok {
		t.Fatal("zero width accepted")
	}
	if _, ok := TierForDimensions(KindAudio, 1280, 720); ok {
		t.Fatal("audio dimensions accepted")
	}
}

func TestCanonicalJSON(t *testing.T) {
	p, err := Parse([]byte(` { "version":1, "unit":"call", "baseUsd":0, "tierMultipliers":null } `))
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.CanonicalString()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"unit":"call","baseUsd":0,"baseTier":"","tierMultipliers":{}}`
	if data != want {
		t.Fatalf("canonical = %s, want %s", data, want)
	}
	clone := p.Clone()
	clone.BaseUsd = 9
	if p.BaseUsd != 0 {
		t.Fatal("Clone shared state")
	}
}

func TestKindForMode(t *testing.T) {
	cases := map[string]Kind{"video": KindVideo, "image": KindImage, "image_generation": KindImage, "audio": KindAudio}
	for mode, want := range cases {
		got, ok := KindForMode(mode)
		if !ok || got != want {
			t.Fatalf("KindForMode(%q) = %q,%v", mode, got, ok)
		}
	}
	for _, mode := range []string{"chat", "responses", "embedding", ""} {
		if _, ok := KindForMode(mode); ok {
			t.Fatalf("mode %q accepted", mode)
		}
	}
	if DefaultUnitForKind(KindVideo) != UnitSecond || DefaultUnitForKind(KindAudio) != UnitSecond || DefaultUnitForKind(KindImage) != UnitImage {
		t.Fatal("unexpected default unit")
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		value  string
		width  int
		height int
		ok     bool
	}{
		{"1024x1024", 1024, 1024, true},
		{" 1280X720 ", 1280, 720, true},
		{"2560*1440", 2560, 1440, true},
		{"auto", 0, 0, false},
		{"", 0, 0, false},
		{"1024", 0, 0, false},
		{"1024x", 0, 0, false},
		{"x1024", 0, 0, false},
		{"0x1024", 0, 0, false},
		{"1024x-1", 0, 0, false},
	}
	for _, tc := range cases {
		width, height, ok := ParseSize(tc.value)
		if width != tc.width || height != tc.height || ok != tc.ok {
			t.Fatalf("ParseSize(%q) = %d,%d,%v want %d,%d,%v", tc.value, width, height, ok, tc.width, tc.height, tc.ok)
		}
	}
}
