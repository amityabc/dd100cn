ALTER TABLE s9a1.accounts ADD COLUMN battle_pass json NOT NULL DEFAULT '{}'::json CHECK (json_typeof(battle_pass)='object');
