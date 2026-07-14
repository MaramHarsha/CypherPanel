-- Reserve the plugins surface (plan.md §11). No loader/runtime yet — this
-- table exists so the schema and API namespace are stable before any plugin
-- ships. The manifest (validated plugin.yaml) is stored as jsonb.

CREATE TABLE plugins (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE,
    version      text NOT NULL,
    kind         text NOT NULL DEFAULT 'plugin'
                 CHECK (kind IN ('plugin', 'theme', 'language_pack')),
    enabled      boolean NOT NULL DEFAULT false,
    manifest     jsonb NOT NULL DEFAULT '{}'::jsonb,
    installed_at timestamptz NOT NULL DEFAULT now()
);
