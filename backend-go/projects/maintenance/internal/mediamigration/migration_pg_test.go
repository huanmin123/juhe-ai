package mediamigration

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const fixtureDDL = `
CREATE SCHEMA juhe_business;
CREATE TABLE juhe_business.models (
  id text PRIMARY KEY,
  vendor_code text,
  public_model text NOT NULL,
  scope text NOT NULL,
  source text,
  media_pricing_json text,
  supported_endpoint_modes_json text NOT NULL DEFAULT '[]',
  output_modalities_json text NOT NULL DEFAULT '[]',
  image_input_usd_per_1m double precision,
  image_output_usd_per_1m double precision,
  audio_input_usd_per_1m double precision,
  audio_output_usd_per_1m double precision,
  output_usd_per_image double precision,
  updated_at text NOT NULL
);`

const fixtureNow = "2026-11-20T00:00:00Z"

func openFixture(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(fixtureDDL)
		return err
	})
}

func insertModel(t *testing.T, db *sql.DB, id, provider, model, source, mediaPricingJSON, endpointModes, outputModalities string, imageIn, imageOut, audioIn, audioOut, perImage any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.models
		(id, vendor_code, public_model, scope, source, media_pricing_json,
		 supported_endpoint_modes_json, output_modalities_json,
		 image_input_usd_per_1m, image_output_usd_per_1m,
		 audio_input_usd_per_1m, audio_output_usd_per_1m,
		 output_usd_per_image, updated_at)
		VALUES ($1,$2,$3,'builtin',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		id, provider, model, source, nullIfEmpty(mediaPricingJSON), endpointModes, outputModalities,
		imageIn, imageOut, audioIn, audioOut, perImage, fixtureNow); err != nil {
		t.Fatalf("insert model %s: %v", id, err)
	}
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func modelMediaPricing(t *testing.T, db *sql.DB, id string) sql.NullString {
	t.Helper()
	var value sql.NullString
	if err := db.QueryRow(`SELECT media_pricing_json FROM juhe_business.models WHERE id = $1`, id).Scan(&value); err != nil {
		t.Fatalf("read media pricing %s: %v", id, err)
	}
	return value
}

func seedFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	insertModel(t, db, "img-per", "gpt", "gpt-image-per", "manual-override", "", `["images_json"]`, `["image"]`, nil, nil, nil, nil, 0.04)
	insertModel(t, db, "img-gemini", "gemini", "gemini-image-token", "manual-override", "", `["generate_content_json"]`, `["text","image"]`, nil, 60, nil, nil, nil)
	insertModel(t, db, "tts-gpt", "gpt", "gpt-tts", "manual-override", "", `["audio_speech"]`, `["audio"]`, nil, nil, nil, 12, nil)
	insertModel(t, db, "stt-gemini", "gemini", "gemini-stt", "manual-override", "", `["audio_transcription_json"]`, `["text"]`, nil, nil, 2, nil, nil)
	insertModel(t, db, "realtime-gpt", "gpt", "gpt-realtime", "manual-override", "", `["realtime_session"]`, `["audio"]`, nil, nil, nil, 64, nil)
	insertModel(t, db, "video-gpt", "gpt", "gpt-video", "manual-override", "", `["video_create"]`, `["video"]`, nil, nil, nil, nil, 0.5)
	insertModel(t, db, "unknown-image", "xai", "xai-image-token", "manual-override", "", `["images_json"]`, `["image"]`, nil, 30, nil, nil, nil)
	insertModel(t, db, "chat-audio", "gpt", "gpt-audio-chat", "manual-override", "", `["chat_json","generate_content_json"]`, `["text","audio"]`, nil, nil, nil, 64, nil)
	insertModel(t, db, "unknown-tts", "qwen", "qwen-tts", "manual-override", "", `["audio_speech"]`, `["audio"]`, nil, nil, nil, 10, nil)
	insertModel(t, db, "undetermined", "qwen", "qwen-audio", "manual-override", "", `[]`, `[]`, nil, nil, nil, 10, nil)
	insertModel(t, db, "image-input", "glm", "glm-chat-image-input", "manual-override", "", `["chat_json"]`, `["text"]`, 1, nil, nil, nil, nil)
	insertModel(t, db, "invalid-modes", "deepseek", "deepseek-invalid", "manual-override", "", `not-json`, `[]`, nil, nil, nil, nil, 0.01)
	insertModel(t, db, "zero-image", "volcengine", "volcengine-free-image", "manual-override", "", `["images_json"]`, `["image"]`, nil, nil, nil, nil, 0.0)
	insertModel(t, db, "non-manual", "gpt", "gpt-catalog-image", "catalog", "", `["images_json"]`, `["image"]`, nil, nil, nil, nil, 0.02)
	insertModel(t, db, "already-priced", "gpt", "gpt-priced-image", "manual-override",
		`{"version":1,"unit":"image","baseUsd":0.03,"baseTier":"1k","tierMultipliers":{"1k":1.0,"2k":1.6,"4k":3.0}}`,
		`["images_json"]`, `["image"]`, nil, nil, nil, nil, 0.03)
}

func TestBuildPlanRequiresMediaPricingColumn(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(`CREATE SCHEMA juhe_business;
CREATE TABLE juhe_business.models (
  id text PRIMARY KEY,
  vendor_code text,
  public_model text NOT NULL,
  scope text NOT NULL,
  source text,
  output_usd_per_image double precision
);`)
		return err
	})
	_, err := BuildPlan(context.Background(), db, Options{DryRun: true})
	if !errors.Is(err, ErrMediaPricingColumnMissing) {
		t.Fatalf("BuildPlan err = %v, want ErrMediaPricingColumnMissing", err)
	}
}

func TestBuildPlanAndApply(t *testing.T) {
	db := openFixture(t)
	seedFixture(t, db)

	catalog := func(providerCode, model string) (string, bool) {
		if providerCode == "gemini" && model == "gemini-stt" {
			return fakeAudioCatalog(0.00005)("", "")
		}
		return "", false
	}
	plan, err := BuildPlan(context.Background(), db, Options{DryRun: true, CatalogMediaPricing: catalog})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Mode != "plan" || !plan.DryRun {
		t.Fatalf("plan mode/dryRun = %s/%t", plan.Mode, plan.DryRun)
	}
	if plan.Scanned != 13 || plan.Convertible != 5 || plan.ManualReview != 4 || plan.Skipped != 4 || plan.Applied != 0 {
		t.Fatalf("plan counts = scanned %d convertible %d manual %d skipped %d applied %d",
			plan.Scanned, plan.Convertible, plan.ManualReview, plan.Skipped, plan.Applied)
	}
	// dry-run 不写库：只有已存在的 already-priced 行非空。
	if got := countNonEmptyMediaPricing(t, db); got != 1 {
		t.Fatalf("dry-run wrote %d rows, want 1 pre-existing", got)
	}

	applied, err := Apply(context.Background(), db, Options{CatalogMediaPricing: catalog})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.Mode != "apply" || applied.DryRun || applied.Applied != 5 {
		t.Fatalf("apply report = mode %s dryRun %t applied %d", applied.Mode, applied.DryRun, applied.Applied)
	}
	assertMediaPricing(t, db, "img-per", mediapricing.UnitImage, 0.04)
	assertMediaPricing(t, db, "img-gemini", mediapricing.UnitImage, 0.0672)
	assertMediaPricing(t, db, "tts-gpt", mediapricing.UnitSecond, 0.0003)
	assertMediaPricing(t, db, "stt-gemini", mediapricing.UnitSecond, 0.00005)
	assertMediaPricing(t, db, "zero-image", mediapricing.UnitImage, 0)
	for _, id := range []string{"realtime-gpt", "video-gpt", "chat-audio", "unknown-image", "unknown-tts", "undetermined", "image-input", "invalid-modes", "non-manual"} {
		if got := modelMediaPricing(t, db, id); got.Valid {
			t.Fatalf("%s media pricing = %q, want NULL", id, got.String)
		}
	}
	if got := modelMediaPricing(t, db, "already-priced"); !got.Valid || got.String == "" {
		t.Fatalf("already-priced media pricing changed: %+v", got)
	}

	var updatedAt string
	if err := db.QueryRow(`SELECT updated_at FROM juhe_business.models WHERE id = 'img-per'`).Scan(&updatedAt); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if updatedAt != fixtureNow {
		t.Fatalf("updated_at = %q, want unchanged %q", updatedAt, fixtureNow)
	}

	rerun, err := Apply(context.Background(), db, Options{CatalogMediaPricing: catalog})
	if err != nil {
		t.Fatalf("Apply rerun: %v", err)
	}
	if rerun.Scanned != 8 || rerun.Convertible != 0 || rerun.Applied != 0 || rerun.ManualReview != 4 || rerun.Skipped != 4 {
		t.Fatalf("rerun counts = scanned %d convertible %d applied %d manual %d skipped %d",
			rerun.Scanned, rerun.Convertible, rerun.Applied, rerun.ManualReview, rerun.Skipped)
	}
}

func countNonEmptyMediaPricing(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.models WHERE media_pricing_json IS NOT NULL AND btrim(media_pricing_json) <> ''`).Scan(&count); err != nil {
		t.Fatalf("count media pricing: %v", err)
	}
	return count
}

func assertMediaPricing(t *testing.T, db *sql.DB, id string, unit mediapricing.Unit, baseUSD float64) {
	t.Helper()
	value := modelMediaPricing(t, db, id)
	if !value.Valid || value.String == "" {
		t.Fatalf("%s media pricing is NULL", id)
	}
	pricing, err := mediapricing.Parse([]byte(value.String))
	if err != nil {
		t.Fatalf("%s parse media pricing %q: %v", id, value.String, err)
	}
	if pricing.Unit != unit || !closeEnough(pricing.BaseUsd, baseUSD) {
		t.Fatalf("%s pricing = %+v, want unit=%s baseUsd=%v", id, pricing, unit, baseUSD)
	}
}
