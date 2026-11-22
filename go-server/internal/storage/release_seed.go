package storage

import (
	"fmt"
	"strings"
)

const ReleaseSeedCera = 1_000_000
const ReleaseSeedPolicy = "release-local-account/v1"

func schemaMigrations() []string {
	return []string{schemaSQL, assetsSQL, premiumSQL, movesSQL, openingSQL, openingCombatSQL, dailyReceiptsSQL, townTransitionSQL,
		commerceHistorySQL, battlePassSQL, treasureGaugeSQL, uniqueAvatarGaugeSQL, characterServicesSQL, fateExchangeSQL, oculusBindingsSQL}
}

// ReleaseSeedSQL is an offline psql input. The advisory lock and CREATE SCHEMA
// keep initialization exclusive and refuse to overwrite an existing game schema.
func ReleaseSeedSQL() []byte {
	var b strings.Builder
	b.WriteString("\\set ON_ERROR_STOP on\nBEGIN;\nSELECT pg_advisory_xact_lock(1395736881,1);\nCREATE SCHEMA s9a1;\n")
	b.WriteString("CREATE TABLE s9a1.schema_version (singleton boolean PRIMARY KEY CHECK (singleton), version integer NOT NULL);\n")
	migrations := schemaMigrations()
	for _, migration := range migrations {
		b.WriteString(migration)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "INSERT INTO s9a1.schema_version VALUES (true,%d);\n", len(migrations))
	fmt.Fprintf(&b, "INSERT INTO s9a1.accounts (account,cera) VALUES ('local',%d);\n", ReleaseSeedCera)
	b.WriteString("CREATE TABLE s9a1.release_seed (policy text PRIMARY KEY, account text NOT NULL REFERENCES s9a1.accounts(account), cera bigint NOT NULL, characters integer NOT NULL);\n")
	fmt.Fprintf(&b, "INSERT INTO s9a1.release_seed VALUES ('%s','local',%d,0);\n", ReleaseSeedPolicy, ReleaseSeedCera)
	fmt.Fprintf(&b, `DO $seed$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM s9a1.accounts WHERE account='local' AND cera=%d AND owner IS NULL
        AND epoch=0 AND phase=5 AND selected_id IS NULL AND asset_revision=0 AND octet_length(game_options)=0 AND octet_length(hotkeys)=0)
        OR EXISTS (SELECT 1 FROM s9a1.characters) THEN
        RAISE EXCEPTION 'Release seed readback failed';
    END IF;
END;
$seed$;
COMMIT;
`, ReleaseSeedCera)
	return []byte(b.String())
}
