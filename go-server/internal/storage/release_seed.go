package storage

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const ReleaseSeedCera = 1_000_000
const ReleaseSeedPolicy = "release-local-account/v1"

//go:embed foundation_state.sql
var foundationStateSQL string

func schemaMigrations() []string {
	return []string{schemaSQL, assetsSQL, premiumSQL, movesSQL, openingSQL, openingCombatSQL, dailyReceiptsSQL, townTransitionSQL,
		commerceHistorySQL, battlePassSQL, treasureGaugeSQL, uniqueAvatarGaugeSQL, characterServicesSQL, fateExchangeSQL, oculusBindingsSQL, foundationStateSQL}
}

// ReleaseSeedSQL is an offline psql input. The advisory lock and CREATE SCHEMA
// keep initialization exclusive and refuse to overwrite an existing game schema.
func ReleaseSeedSQL(buildID uuid.UUID) []byte {
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
	fmt.Fprintf(&b, "INSERT INTO s9a1.release_seed(policy,account,cera,characters) VALUES ('%s','local',%d,0);\n", ReleaseSeedPolicy, ReleaseSeedCera)
	receipt := fmt.Sprintf(`{"Policy":"%s","Account":"local","Cera":%d,"Characters":0,"Build":"%s"}`, ReleaseSeedPolicy, ReleaseSeedCera, buildID)
	fmt.Fprintf(&b, "INSERT INTO s9a1.foundation_state(account,state_key,state_value) VALUES ('local',convert_to('%s','UTF8'),convert_to('%s','UTF8'));\n", ReleaseSeedPolicy, receipt)
	fmt.Fprintf(&b, `DO $seed$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM s9a1.accounts WHERE account='local' AND cera=%d AND owner IS NULL
        AND epoch=0 AND phase=5 AND selected_id IS NULL AND asset_revision=0 AND octet_length(game_options)=0 AND octet_length(hotkeys)=0)
        OR EXISTS (SELECT 1 FROM s9a1.characters)
        OR NOT EXISTS (SELECT 1 FROM s9a1.release_seed r JOIN s9a1.foundation_state f
            ON f.account=r.account AND f.state_key=convert_to(r.policy, 'UTF8')
            WHERE r.account='local' AND r.policy='%s' AND r.cera=%d AND r.characters=0
                AND f.state_value=convert_to('%s','UTF8') AND f.source_digest IS NULL) THEN
        RAISE EXCEPTION 'Release seed readback failed';
    END IF;
END;
$seed$;
COMMIT;
`, ReleaseSeedCera, ReleaseSeedPolicy, ReleaseSeedCera, receipt)
	return []byte(b.String())
}
