-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_dataset.operation_logs (
  id text PRIMARY KEY,
  trace_id text,
  actor_system_account_id text NOT NULL,
  actor_username text,
  actor_display_name text,
  actor_role text NOT NULL,
  operation_scope_system_account_id text,
  mode text NOT NULL DEFAULT 'self' CHECK (mode IN ('self', 'admin')),
  module text NOT NULL,
  action text NOT NULL,
  operation_key text NOT NULL,
  resource_type text NOT NULL,
  resource_id text,
  resource_name text,
  summary text NOT NULL,
  detail_level text NOT NULL DEFAULT 'full' CHECK (detail_level IN ('full', 'summary')),
  visibility_scope text NOT NULL DEFAULT 'targeted' CHECK (visibility_scope IN ('targeted', 'all_users', 'admin_only')),
  changes_json text NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(changes_json::jsonb) = 'array'),
  metadata_json text NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata_json::jsonb) = 'object'),
  method text,
  path text,
  status_code integer,
  client_ip text,
  user_agent text,
  created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_dataset.operation_log_targets (
  id text PRIMARY KEY,
  operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,
  target_type text NOT NULL,
  target_id text,
  target_name text,
  target_owner_system_account_id text,
  relation text NOT NULL DEFAULT 'affected' CHECK (relation IN ('primary', 'affected', 'created', 'deleted', 'owner', 'grantee', 'team_member', 'bound_resource')),
  created_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS juhe_dataset.operation_log_viewers (
  operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,
  system_account_id text NOT NULL,
  visibility_reason text NOT NULL CHECK (visibility_reason IN ('actor_self', 'resource_owner', 'admin_managed_my_resource', 'authorization_owner', 'authorization_grantee', 'team_member', 'team_authorization', 'global_affected', 'bound_resource_affected')),
  detail_level text NOT NULL DEFAULT 'full' CHECK (detail_level IN ('full', 'summary')),
  created_at timestamptz NOT NULL,
  PRIMARY KEY (operation_log_id, system_account_id, visibility_reason)
);

CREATE TABLE IF NOT EXISTS juhe_dataset.operation_log_summary_search_terms (
  operation_log_id text NOT NULL REFERENCES juhe_dataset.operation_logs(id) ON DELETE CASCADE,
  term text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (term, operation_log_id)
);

CREATE INDEX IF NOT EXISTS idx_operation_logs_created
  ON juhe_dataset.operation_logs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_logs_actor_created
  ON juhe_dataset.operation_logs (actor_system_account_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_logs_scope_created
  ON juhe_dataset.operation_logs (operation_scope_system_account_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_logs_module_action_created
  ON juhe_dataset.operation_logs (module, action, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_logs_resource_created
  ON juhe_dataset.operation_logs (resource_type, resource_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_logs_resource_id_created
  ON juhe_dataset.operation_logs (resource_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_logs_visibility_created
  ON juhe_dataset.operation_logs (visibility_scope, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_logs_trace_id
  ON juhe_dataset.operation_logs (trace_id);
CREATE INDEX IF NOT EXISTS idx_operation_logs_trace_c_created
  ON juhe_dataset.operation_logs ((trace_id COLLATE "C"), created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_log_targets_target
  ON juhe_dataset.operation_log_targets (target_type, target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_operation_log_targets_log_created
  ON juhe_dataset.operation_log_targets (operation_log_id, created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_operation_log_viewers_account_created
  ON juhe_dataset.operation_log_viewers (system_account_id, created_at DESC, operation_log_id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_log_viewers_account_log
  ON juhe_dataset.operation_log_viewers (system_account_id, operation_log_id);
CREATE INDEX IF NOT EXISTS idx_operation_log_viewers_log_account
  ON juhe_dataset.operation_log_viewers (operation_log_id, system_account_id);
CREATE INDEX IF NOT EXISTS idx_operation_log_summary_search_terms_term_created
  ON juhe_dataset.operation_log_summary_search_terms (term, created_at DESC, operation_log_id DESC);
CREATE INDEX IF NOT EXISTS idx_operation_log_summary_search_terms_log
  ON juhe_dataset.operation_log_summary_search_terms (operation_log_id);

-- +goose Down
-- no-op: operation logs are append-only audit facts.
