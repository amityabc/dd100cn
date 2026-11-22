ALTER TABLE s9a1.accounts ADD COLUMN unique_avatar_gauge integer NOT NULL DEFAULT 0
    CHECK (unique_avatar_gauge BETWEEN 0 AND 1000);
