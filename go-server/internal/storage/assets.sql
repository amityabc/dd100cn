ALTER TABLE s9a1.accounts ADD COLUMN cargo_capacity integer NOT NULL DEFAULT 0 CHECK (cargo_capacity BETWEEN 0 AND 256);
ALTER TABLE s9a1.characters
    ADD COLUMN bag_expansion integer NOT NULL DEFAULT 0 CHECK (bag_expansion IN (0,8,16,24)),
    ADD COLUMN personal_capacity integer NOT NULL DEFAULT 152 CHECK (personal_capacity BETWEEN 0 AND 264),
    ADD COLUMN legacy_personal_capacity integer NOT NULL DEFAULT 152 CHECK (legacy_personal_capacity BETWEEN 0 AND 152);

CREATE TABLE s9a1.assets (
    instance_id text PRIMARY KEY CHECK (instance_id ~ '^[0-9a-fA-F]{32}$' AND instance_id<>'00000000000000000000000000000000'),
    account text NOT NULL REFERENCES s9a1.accounts(account),
    character_id integer,
    source text NOT NULL CHECK (source IN ('weapon','item','shared')),
    template_id bigint NOT NULL CHECK(template_id BETWEEN 1 AND 4294967294),
    template_json json NOT NULL,
    weapon_facts json NOT NULL DEFAULT 'null',
    count bigint NOT NULL CHECK (count BETWEEN 1 AND 4294967295),
    location smallint NOT NULL CHECK (location IN (0,2,3,7,12,38,44,45)),
    slot integer NOT NULL CHECK (slot BETWEEN 0 AND 65535),
    attributes_json json NOT NULL,
    core bytea NOT NULL CHECK (octet_length(core)=119),
    detail bytea NOT NULL CHECK (octet_length(detail)<=65536),
    CHECK ((character_id IS NULL)=(location IN (12,44,45) OR source='shared')),
    CHECK (source<>'shared' OR (location=0 AND slot BETWEEN 363 AND 374)),
    CHECK (source<>'weapon' OR (count=1 AND json_typeof(weapon_facts)='object')),
    FOREIGN KEY (account,character_id) REFERENCES s9a1.characters(account,wire_id)
);
CREATE UNIQUE INDEX asset_uid_case ON s9a1.assets(lower(instance_id));
CREATE UNIQUE INDEX asset_slot ON s9a1.assets(account,coalesce(character_id,0),location,slot);
CREATE INDEX equipped_assets ON s9a1.assets(account,character_id,slot) WHERE location=3;

CREATE TABLE s9a1.reward_receipts (
    character_id integer NOT NULL REFERENCES s9a1.characters(wire_id),
    event_key text NOT NULL CHECK(event_key<>''),
    payload json NOT NULL CHECK(json_typeof(payload)='object'),
    PRIMARY KEY(character_id,event_key)
);
