CREATE TABLE s9a1.opening_entry (
    account text PRIMARY KEY REFERENCES s9a1.accounts(account),
    owner uuid NOT NULL,
    epoch bigint NOT NULL,
    character_id integer NOT NULL REFERENCES s9a1.characters(wire_id),
    resource_hash text NOT NULL,
    cost integer NOT NULL CHECK(cost BETWEEN 0 AND 65535)
);
