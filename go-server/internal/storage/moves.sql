-- The original UID survives both sides of a swap. Defer only slot uniqueness
-- during this transaction so both UPDATEs can finish without a parking slot.
ALTER TABLE s9a1.assets ADD CONSTRAINT asset_position
    UNIQUE NULLS NOT DISTINCT (account,character_id,location,slot) DEFERRABLE INITIALLY IMMEDIATE;
DROP INDEX s9a1.asset_slot;

CREATE TABLE s9a1.last_inventory_move (
    account text PRIMARY KEY REFERENCES s9a1.accounts(account),
    owner uuid NOT NULL,
    epoch bigint NOT NULL,
    character_id integer NOT NULL REFERENCES s9a1.characters(wire_id),
    revision bigint NOT NULL,
    account_revision bigint NOT NULL,
    sequence integer NOT NULL CHECK(sequence BETWEEN 0 AND 65535),
    body bytea NOT NULL CHECK(octet_length(body)=28),
    destination integer NOT NULL CHECK(destination BETWEEN 0 AND 65535)
);
