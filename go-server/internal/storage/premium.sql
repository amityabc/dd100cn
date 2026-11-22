ALTER TABLE s9a1.accounts
    ADD COLUMN conqueror_expires_at bigint NOT NULL DEFAULT 0 CHECK(conqueror_expires_at BETWEEN 0 AND 253402300799),
    ADD COLUMN skill_master_expires_at bigint NOT NULL DEFAULT 0 CHECK(skill_master_expires_at BETWEEN 0 AND 253402300799),
    ADD COLUMN crystal_expires_at bigint NOT NULL DEFAULT 0 CHECK(crystal_expires_at BETWEEN 0 AND 253402300799),
    ADD COLUMN growth_expires_at bigint NOT NULL DEFAULT 0 CHECK(growth_expires_at BETWEEN 0 AND 253402300799);
