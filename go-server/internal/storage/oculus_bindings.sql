ALTER TABLE s9a1.accounts ADD COLUMN oculus_guide_bindings_json jsonb NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(oculus_guide_bindings_json) = 'array');
