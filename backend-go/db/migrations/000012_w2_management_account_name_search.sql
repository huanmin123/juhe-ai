-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.account_name_search_documents (
  account_id text PRIMARY KEY,
  system_account_id text NOT NULL,
  normalized_name text NOT NULL CHECK (btrim(normalized_name) <> ''),
  updated_at timestamptz NOT NULL,
  FOREIGN KEY (account_id, system_account_id)
    REFERENCES juhe_business.accounts(id, system_account_id)
    ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS juhe_business.account_name_search_terms (
  account_id text NOT NULL,
  system_account_id text NOT NULL,
  term text NOT NULL CHECK (btrim(term) <> ''),
  created_at timestamptz NOT NULL,
  PRIMARY KEY (account_id, term),
  FOREIGN KEY (account_id, system_account_id)
    REFERENCES juhe_business.accounts(id, system_account_id)
    ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_term_owner
  ON juhe_business.account_name_search_terms(term, system_account_id, account_id);
CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_owner_term
  ON juhe_business.account_name_search_terms(system_account_id, term, account_id);
CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_account
  ON juhe_business.account_name_search_terms(account_id);
CREATE INDEX IF NOT EXISTS idx_account_name_search_documents_owner
  ON juhe_business.account_name_search_documents(system_account_id, account_id);

-- +goose Down
-- no-op: account name search terms are derived business indexes and may be rebuilt offline.
