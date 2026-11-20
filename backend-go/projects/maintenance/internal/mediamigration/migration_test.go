package mediamigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

func nullFloat(value float64) sql.NullFloat64 {
	return sql.NullFloat64{Float64: value, Valid: true}
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func sourceOf(id string, modes string, outputModalities string) sourceRow {
	return sourceRow{
		ID:                   id,
		ProviderCode:         "gpt",
		Model:                "test-model",
		Scope:                "builtin",
		EndpointModesJSON:    nullString(modes),
		OutputModalitiesJSON: nullString(outputModalities),
	}
}

func TestClassifyMediaKind(t *testing.T) {
	cases := []struct {
		name     string
		source   sourceRow
		wantKind string
		wantWhy  string
	}{
		{
			name:     "images mode",
			source:   sourceOf("a", `["images_json"]`, `["image"]`),
			wantKind: KindImage,
		},
		{
			name: "generate content image by output modality",
			source: func() sourceRow {
				row := sourceOf("b", `["generate_content_json"]`, `["text","image"]`)
				row.ImageOutputUsdPer1M = nullFloat(60)
				return row
			}(),
			wantKind: KindImage,
		},
		{
			name:     "empty modes image price",
			source:   func() sourceRow { row := sourceOf("c", ``, `[]`); row.OutputUsdPerImage = nullFloat(0.04); return row }(),
			wantKind: KindImage,
		},
		{
			name: "image price without image output",
			source: func() sourceRow {
				row := sourceOf("d", `["chat_json"]`, `["text"]`)
				row.ImageOutputUsdPer1M = nullFloat(30)
				return row
			}(),
			wantKind: KindUnknown,
			wantWhy:  "image_output_price_without_image_output",
		},
		{
			name:     "tts",
			source:   sourceOf("e", `["audio_speech"]`, `["audio"]`),
			wantKind: KindTTS,
		},
		{
			name:     "stt",
			source:   sourceOf("f", `["audio_transcription_json"]`, `["text"]`),
			wantKind: KindSTT,
		},
		{
			name:     "realtime",
			source:   sourceOf("g", `["realtime_session"]`, `["audio"]`),
			wantKind: KindRealtime,
			wantWhy:  "realtime_stays_legacy",
		},
		{
			name:     "video",
			source:   sourceOf("h", `["video_create"]`, `["video"]`),
			wantKind: KindVideo,
			wantWhy:  "video_has_no_db_legacy_price",
		},
		{
			name:     "ambiguous audio",
			source:   sourceOf("i", `["audio_speech","audio_transcription_json"]`, `["audio"]`),
			wantKind: KindUnknown,
			wantWhy:  "ambiguous_media_modes",
		},
		{
			name:     "invalid modes",
			source:   sourceOf("j", `not-json`, `[]`),
			wantKind: KindUnknown,
			wantWhy:  "invalid_endpoint_modes",
		},
		{
			name: "audio price without modes",
			source: func() sourceRow {
				row := sourceOf("k", ``, `[]`)
				row.AudioOutputUsdPer1M = nullFloat(10)
				return row
			}(),
			wantKind: KindUnknown,
			wantWhy:  "media_kind_undetermined",
		},
		{
			name: "chat audio stays legacy",
			source: func() sourceRow {
				row := sourceOf("m", `["chat_json","generate_content_json"]`, `["text","audio"]`)
				row.AudioOutputUsdPer1M = nullFloat(64)
				return row
			}(),
			wantKind: KindNonMedia,
			wantWhy:  "chat_audio_stays_legacy",
		},
		{
			name: "image input only",
			source: func() sourceRow {
				row := sourceOf("l", `["chat_json"]`, `["text"]`)
				row.ImageInputUsdPer1M = nullFloat(1)
				return row
			}(),
			wantKind: KindNonMedia,
			wantWhy:  "image_input_only",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gotKind, gotWhy := classify(testCase.source)
			if gotKind != testCase.wantKind || gotWhy != testCase.wantWhy {
				t.Fatalf("classify = %q/%q, want %q/%q", gotKind, gotWhy, testCase.wantKind, testCase.wantWhy)
			}
		})
	}
}

func TestDecideConvertsImagePerImage(t *testing.T) {
	source := sourceOf("img", `["images_json"]`, `["image"]`)
	source.OutputUsdPerImage = nullFloat(0.04)

	row := decide(source)
	if row.Action != ActionConvert || row.Kind != KindImage {
		t.Fatalf("decide = %s/%s, want convert/image", row.Action, row.Kind)
	}
	assertPricing(t, row.MediaPricingJSON, mediapricing.UnitImage, 0.04, mediapricing.ImageTier1K)
	if row.Formula != "output_usd_per_image" {
		t.Fatalf("formula = %q", row.Formula)
	}
}

func TestDecideConvertsImageTokenByProvider(t *testing.T) {
	cases := []struct {
		provider string
		tokenUSD float64
		want     float64
	}{
		{provider: "gemini", tokenUSD: 60, want: 0.0672},
		{provider: "gpt", tokenUSD: 30, want: 0.03168},
	}
	for _, testCase := range cases {
		source := sourceOf("img-"+testCase.provider, `["generate_content_json"]`, `["text","image"]`)
		source.ProviderCode = testCase.provider
		source.ImageOutputUsdPer1M = nullFloat(testCase.tokenUSD)

		row := decide(source)
		if row.Action != ActionConvert {
			t.Fatalf("%s action = %s reason = %s", testCase.provider, row.Action, row.Reason)
		}
		assertPricing(t, row.MediaPricingJSON, mediapricing.UnitImage, testCase.want, mediapricing.ImageTier1K)
	}
}

func TestDecideRejectsUnknownImageTokenConstant(t *testing.T) {
	source := sourceOf("img-xai", `["images_json"]`, `["image"]`)
	source.ProviderCode = "xai"
	source.ImageOutputUsdPer1M = nullFloat(30)

	row := decide(source)
	if row.Action != ActionManualReview || row.Reason != "unknown_image_token_constant" {
		t.Fatalf("decide = %s/%s", row.Action, row.Reason)
	}
	if row.MediaPricingJSON != "" {
		t.Fatalf("manual review must not carry pricing: %q", row.MediaPricingJSON)
	}
}

func TestDecideConvertsFreeImagePrice(t *testing.T) {
	source := sourceOf("img-free", `["images_json"]`, `["image"]`)
	source.OutputUsdPerImage = nullFloat(0)

	row := decide(source)
	if row.Action != ActionConvert {
		t.Fatalf("action = %s reason = %s", row.Action, row.Reason)
	}
	assertPricing(t, row.MediaPricingJSON, mediapricing.UnitImage, 0, mediapricing.ImageTier1K)
}

func TestDecideRejectsNegativeImagePrice(t *testing.T) {
	source := sourceOf("img-negative", `["images_json"]`, `["image"]`)
	source.OutputUsdPerImage = nullFloat(-0.01)

	row := decide(source)
	if row.Action != ActionManualReview || row.Reason != "invalid_legacy_price" {
		t.Fatalf("decide = %s/%s", row.Action, row.Reason)
	}
}

func TestDecideConvertsTTSAndSTT(t *testing.T) {
	tts := sourceOf("tts", `["audio_speech"]`, `["audio"]`)
	tts.AudioOutputUsdPer1M = nullFloat(12)
	ttsRow := decide(tts)
	if ttsRow.Action != ActionConvert || ttsRow.Kind != KindTTS {
		t.Fatalf("tts decide = %s/%s reason=%s", ttsRow.Action, ttsRow.Kind, ttsRow.Reason)
	}
	assertPricing(t, ttsRow.MediaPricingJSON, mediapricing.UnitSecond, 0.0003, "")

	stt := sourceOf("stt", `["audio_transcription_json"]`, `["text"]`)
	stt.AudioInputUsdPer1M = nullFloat(2)
	sttRow := decideWithCatalog(stt, fakeAudioCatalog(0.00005))
	if sttRow.Action != ActionConvert || sttRow.Kind != KindSTT {
		t.Fatalf("stt decide = %s/%s reason=%s", sttRow.Action, sttRow.Kind, sttRow.Reason)
	}
	assertPricing(t, sttRow.MediaPricingJSON, mediapricing.UnitSecond, 0.00005, "")
}

func TestDecideSTTUsesCatalogSecondsOnlyWhenTokenDerivedMatches(t *testing.T) {
	source := sourceOf("stt", `["audio_transcription_json"]`, `["text"]`)
	source.AudioInputUsdPer1M = nullFloat(6)

	row := decideWithCatalog(source, fakeAudioCatalog(0.0001))
	if row.Action != ActionManualReview || row.Reason != "stt_seconds_price_ambiguous" {
		t.Fatalf("decide = %s/%s, want manual_review/stt_seconds_price_ambiguous", row.Action, row.Reason)
	}
	noCatalog := decide(source)
	if noCatalog.Action != ActionManualReview || noCatalog.Reason != "stt_seconds_price_ambiguous" {
		t.Fatalf("no catalog decide = %s/%s", noCatalog.Action, noCatalog.Reason)
	}
}

func fakeAudioCatalog(baseUSD float64) func(string, string) (string, bool) {
	return func(string, string) (string, bool) {
		pricing := &mediapricing.Pricing{
			Version:         mediapricing.Version,
			Unit:            mediapricing.UnitSecond,
			BaseUsd:         baseUSD,
			TierMultipliers: map[string]float64{},
		}
		canonical, err := pricing.CanonicalString()
		if err != nil {
			return "", false
		}
		return canonical, true
	}
}

func TestDecideUnknownAudioTokenProviderIsManualReview(t *testing.T) {
	source := sourceOf("tts-qwen", `["audio_speech"]`, `["audio"]`)
	source.ProviderCode = "qwen"
	source.AudioOutputUsdPer1M = nullFloat(10)

	row := decide(source)
	if row.Action != ActionManualReview || row.Reason != "unknown_audio_token_constant" {
		t.Fatalf("decide = %s/%s", row.Action, row.Reason)
	}
}

func TestApplyRejectsDryRunOption(t *testing.T) {
	_, err := Apply(context.Background(), nil, Options{DryRun: true})
	if !errors.Is(err, ErrApplyRequiresWrites) {
		t.Fatalf("Apply err = %v, want ErrApplyRequiresWrites", err)
	}
}

func TestDecideAudioMissingPriceIsManualReview(t *testing.T) {
	tts := decide(sourceOf("tts", `["audio_speech"]`, `["audio"]`))
	if tts.Action != ActionManualReview || tts.Reason != "tts_output_price_missing" {
		t.Fatalf("tts = %s/%s", tts.Action, tts.Reason)
	}
	stt := decide(sourceOf("stt", `["audio_job_create"]`, `["text"]`))
	if stt.Action != ActionManualReview || stt.Reason != "stt_input_price_missing" {
		t.Fatalf("stt = %s/%s", stt.Action, stt.Reason)
	}
}

func TestDecideSkipsRealtimeVideoAndNonMedia(t *testing.T) {
	cases := []struct {
		source sourceRow
		action string
	}{
		{source: func() sourceRow {
			row := sourceOf("rt", `["realtime_session"]`, `["audio"]`)
			row.AudioOutputUsdPer1M = nullFloat(64)
			return row
		}(), action: ActionSkip},
		{source: func() sourceRow {
			row := sourceOf("video", `["video_create"]`, `["video"]`)
			row.OutputUsdPerImage = nullFloat(0.5)
			return row
		}(), action: ActionSkip},
	}
	for _, testCase := range cases {
		row := decide(testCase.source)
		if row.Action != testCase.action {
			t.Fatalf("%s action = %s reason = %s, want %s", row.ID, row.Action, row.Reason, testCase.action)
		}
		if row.MediaPricingJSON != "" {
			t.Fatalf("%s must not carry pricing", row.ID)
		}
	}
}

func TestDecideImageInputOnlyGoesManualReview(t *testing.T) {
	source := sourceOf("input", `["chat_json"]`, `["text"]`)
	source.ImageInputUsdPer1M = nullFloat(1)

	row := decide(source)
	if row.Action != ActionManualReview || row.Reason != "image_input_only" {
		t.Fatalf("decide = %s/%s, want manual_review/image_input_only", row.Action, row.Reason)
	}
	if row.MediaPricingJSON != "" {
		t.Fatal("manual_review row must not carry pricing")
	}
}

func TestDecideRejectsConvertedPriceOverLimit(t *testing.T) {
	source := sourceOf("huge", `["images_json"]`, `["image"]`)
	source.OutputUsdPerImage = nullFloat(1001)

	row := decide(source)
	if row.Action != ActionManualReview || row.Reason != "invalid_converted_price" {
		t.Fatalf("decide = %s/%s", row.Action, row.Reason)
	}
}

func assertPricing(t *testing.T, canonical string, unit mediapricing.Unit, baseUSD float64, baseTier string) {
	t.Helper()
	if strings.TrimSpace(canonical) == "" {
		t.Fatal("empty media pricing")
	}
	pricing, err := mediapricing.Parse([]byte(canonical))
	if err != nil {
		t.Fatalf("parse media pricing %q: %v", canonical, err)
	}
	if pricing.Unit != unit || !closeEnough(pricing.BaseUsd, baseUSD) || pricing.BaseTier != baseTier {
		t.Fatalf("pricing = %+v, want unit=%s baseUsd=%v baseTier=%s", pricing, unit, baseUSD, baseTier)
	}
	if unit == mediapricing.UnitImage {
		if len(pricing.TierMultipliers) != len(mediapricing.ImageTiers) {
			t.Fatalf("image tiers = %v", pricing.TierMultipliers)
		}
	}
	if unit == mediapricing.UnitSecond && len(pricing.TierMultipliers) != 0 {
		t.Fatalf("audio tiers = %v", pricing.TierMultipliers)
	}
}

func closeEnough(left, right float64) bool {
	diff := left - right
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1e-12
}

func TestPlanManifestHandlesNonFiniteLegacyValues(t *testing.T) {
	source := sourceOf("nan", `["images_json"]`, `["image"]`)
	source.OutputUsdPerImage = nullFloat(math.NaN())

	row := decide(source)
	if row.Action != ActionManualReview || row.Reason != "invalid_legacy_price" {
		t.Fatalf("decide = %s/%s", row.Action, row.Reason)
	}
	plan := Plan{Database: "test", Mode: "plan", DryRun: true, Scanned: 1, ManualReview: 1, Rows: []Row{row}}
	if _, err := json.Marshal(plan); err != nil {
		t.Fatalf("marshal plan with NaN legacy value: %v", err)
	}
}
