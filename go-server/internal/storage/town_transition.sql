CREATE TABLE s9a1.town_transition (
    account text PRIMARY KEY REFERENCES s9a1.accounts(account),
    owner uuid NOT NULL,
    epoch bigint NOT NULL,
    character_id integer NOT NULL REFERENCES s9a1.characters(wire_id),
    trace json NOT NULL CHECK(json_typeof(trace)='object')
);
