CREATE TABLE s9a1.accounts (
    account text PRIMARY KEY CHECK (account <> ''),
    owner uuid,
    epoch bigint NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    phase smallint NOT NULL DEFAULT 5 CHECK (phase BETWEEN 0 AND 5),
    selected_id integer,
    capacity smallint NOT NULL DEFAULT 32 CHECK (capacity BETWEEN 32 AND 100),
    cera bigint NOT NULL DEFAULT 0 CHECK (cera >= 0),
    cargo_gold bigint NOT NULL DEFAULT 0 CHECK (cargo_gold BETWEEN 0 AND 2147483647),
    asset_revision bigint NOT NULL DEFAULT 0 CHECK (asset_revision >= 0),
    game_options bytea NOT NULL DEFAULT ''::bytea CHECK (octet_length(game_options) IN (0,372)),
    hotkeys bytea NOT NULL DEFAULT ''::bytea CHECK (octet_length(hotkeys) < 1400)
);

-- A transactionally reserved wire ID cannot exceed the original U16 or be
-- consumed by a failed character creation. Database row IDs are independent.
CREATE TABLE s9a1.character_id_allocator (
    singleton boolean PRIMARY KEY CHECK (singleton),
    next_id integer NOT NULL CHECK (next_id BETWEEN 1 AND 65536)
);
INSERT INTO s9a1.character_id_allocator VALUES (true, 1);

CREATE TABLE s9a1.characters (
    row_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    wire_id integer NOT NULL UNIQUE CHECK (wire_id BETWEEN 1 AND 65535),
    account text NOT NULL REFERENCES s9a1.accounts(account),
    storage_slot smallint NOT NULL CHECK (storage_slot BETWEEN 0 AND 99),
    name text NOT NULL CHECK (char_length(name) BETWEEN 2 AND 30),
    name_key text COLLATE "C" NOT NULL UNIQUE,
    job smallint NOT NULL CHECK (job BETWEEN 0 AND 15),
    level smallint NOT NULL CHECK (level BETWEEN 1 AND 255),
    created bigint NOT NULL CHECK (created BETWEEN 0 AND 4294967295),
    revision bigint NOT NULL CHECK (revision >= 0),
    stats_json json NOT NULL,
    gameplay_json json NOT NULL,
    resource_hash text NOT NULL CHECK (resource_hash ~ '^[0-9a-fA-F]{64}$'),
    town integer NOT NULL CHECK (town BETWEEN 0 AND 65535),
    area integer NOT NULL CHECK (area BETWEEN 0 AND 65535),
    x integer NOT NULL CHECK (x BETWEEN 0 AND 65535),
    y integer NOT NULL CHECK (y BETWEEN 0 AND 65535),
    direction smallint NOT NULL CHECK (direction BETWEEN 0 AND 7),
    state smallint NOT NULL CHECK (state BETWEEN 0 AND 255),
    wire_tail integer NOT NULL CHECK (wire_tail BETWEEN 0 AND 65535),
    return_town integer CHECK (return_town BETWEEN 0 AND 65535),
    return_area integer CHECK (return_area BETWEEN 0 AND 65535),
    return_x integer CHECK (return_x BETWEEN 0 AND 65535),
    return_y integer CHECK (return_y BETWEEN 0 AND 65535),
    return_direction smallint CHECK (return_direction BETWEEN 0 AND 7),
    gold bigint NOT NULL DEFAULT 0 CHECK (gold BETWEEN 0 AND 4294967295),
    coin bigint NOT NULL DEFAULT 10 CHECK (coin BETWEEN 0 AND 4294967295),
    CHECK (num_nonnulls(return_town,return_area,return_x,return_y,return_direction) IN (0,5)),
    UNIQUE (account,storage_slot),
    UNIQUE (account,wire_id)
);
ALTER TABLE s9a1.accounts ADD CONSTRAINT selected_character_owner
    FOREIGN KEY (account,selected_id) REFERENCES s9a1.characters(account,wire_id);

CREATE TABLE s9a1.name_reservations (
    name_key text COLLATE "C" PRIMARY KEY,
    character_id integer NOT NULL REFERENCES s9a1.characters(wire_id),
    expires_at bigint NOT NULL CHECK (expires_at > 0)
);

CREATE TABLE s9a1.gold_receipts (
    character_id integer NOT NULL REFERENCES s9a1.characters(wire_id),
    operation text NOT NULL CHECK (operation <> ''),
    delta bigint NOT NULL CHECK (delta BETWEEN -4294967295 AND 4294967295 AND delta <> 0),
    PRIMARY KEY (character_id,operation)
);
