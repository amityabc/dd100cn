CREATE TABLE s9a1.commerce_purchases (
    account text NOT NULL REFERENCES s9a1.accounts(account),
    restriction_id bigint NOT NULL CHECK (restriction_id BETWEEN 1 AND 4294967295),
    period_start bigint NOT NULL CHECK (period_start >= 0),
    count bigint NOT NULL CHECK (count BETWEEN 1 AND 4294967295),
    PRIMARY KEY (account, restriction_id, period_start)
);
ALTER TABLE s9a1.accounts ADD COLUMN pet_serial_next bigint NOT NULL DEFAULT 1
    CHECK (pet_serial_next BETWEEN 1 AND 4294967296);
