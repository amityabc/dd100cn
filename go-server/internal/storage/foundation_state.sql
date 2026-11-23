CREATE TABLE s9a1.foundation_state (
    account text NOT NULL REFERENCES s9a1.accounts(account),
    state_key bytea NOT NULL CHECK (octet_length(state_key) > 0),
    key_sha256 bytea GENERATED ALWAYS AS (sha256(state_key)) STORED,
    state_value bytea NOT NULL,
    source_digest text CHECK (source_digest ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY (account, key_sha256)
);

-- Earlier PostgreSQL distribution seeds created this status table directly.
CREATE TABLE IF NOT EXISTS s9a1.release_seed (
    policy text NOT NULL,
    account text NOT NULL REFERENCES s9a1.accounts(account),
    cera bigint NOT NULL,
    characters integer NOT NULL
);
ALTER TABLE s9a1.release_seed DROP CONSTRAINT IF EXISTS release_seed_pkey;
ALTER TABLE s9a1.release_seed ADD PRIMARY KEY (account, policy);

INSERT INTO s9a1.foundation_state(account, state_key, state_value)
SELECT account, convert_to(policy, 'UTF8'),
    convert_to(json_build_object('Policy', policy, 'Account', account, 'Cera', cera, 'Characters', characters)::text, 'UTF8')
FROM s9a1.release_seed;
