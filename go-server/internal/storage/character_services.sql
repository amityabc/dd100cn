CREATE TABLE s9a1.character_service_deadlines (
    account text NOT NULL REFERENCES s9a1.accounts(account),
    slot integer NOT NULL CHECK (slot BETWEEN 0 AND 7),
    expires_at bigint NOT NULL CHECK (expires_at BETWEEN 1 AND 4294967295),
    PRIMARY KEY (account, slot)
);
