-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.account_tags (
  id text PRIMARY KEY,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (btrim(name) <> ''),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE (id, system_account_id),
  UNIQUE (system_account_id, name)
);

CREATE TABLE IF NOT EXISTS juhe_business.account_tag_bindings (
  account_id text NOT NULL,
  tag_id text NOT NULL,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (account_id, tag_id),
  FOREIGN KEY (account_id, system_account_id)
    REFERENCES juhe_business.accounts(id, system_account_id)
    ON DELETE CASCADE,
  FOREIGN KEY (tag_id, system_account_id)
    REFERENCES juhe_business.account_tags(id, system_account_id)
    ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_account_tags_owner_name_lookup
  ON juhe_business.account_tags(system_account_id, name COLLATE "C", id);
CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_owner_tag
  ON juhe_business.account_tag_bindings(system_account_id, tag_id, account_id);
CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_tag_owner
  ON juhe_business.account_tag_bindings(tag_id, system_account_id, account_id);
CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_tag
  ON juhe_business.account_tag_bindings(tag_id, account_id);

-- +goose Down
-- no-op: account tags are business data.
