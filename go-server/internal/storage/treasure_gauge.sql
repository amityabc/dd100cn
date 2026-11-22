ALTER TABLE s9a1.accounts ADD COLUMN treasure_gauge bigint NOT NULL DEFAULT 0
    CHECK (treasure_gauge BETWEEN 0 AND 2147483647);
