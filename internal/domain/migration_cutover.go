package domain

// MigrationCutoverSubphase is a fixed durable intent marker. It never stores
// engine output, IDs, addresses, or other runtime-provided values.
type MigrationCutoverSubphase string

const (
	MigrationCutoverSubphaseNone                 MigrationCutoverSubphase = ""
	MigrationCutoverSubphaseBeforeOldStop        MigrationCutoverSubphase = "before_old_stop"
	MigrationCutoverSubphaseBeforePreparedStop   MigrationCutoverSubphase = "before_prepared_stop"
	MigrationCutoverSubphaseBeforePreparedRemove MigrationCutoverSubphase = "before_prepared_remove"
	MigrationCutoverSubphaseBeforeFinalCreate    MigrationCutoverSubphase = "before_final_create"
	MigrationCutoverSubphaseBeforeFinalStart     MigrationCutoverSubphase = "before_final_start"
	MigrationCutoverSubphaseBeforeCommit         MigrationCutoverSubphase = "before_commit"
)

func IsMigrationCutoverSubphase(value MigrationCutoverSubphase) bool {
	switch value {
	case MigrationCutoverSubphaseNone,
		MigrationCutoverSubphaseBeforeOldStop,
		MigrationCutoverSubphaseBeforePreparedStop,
		MigrationCutoverSubphaseBeforePreparedRemove,
		MigrationCutoverSubphaseBeforeFinalCreate,
		MigrationCutoverSubphaseBeforeFinalStart,
		MigrationCutoverSubphaseBeforeCommit:
		return true
	default:
		return false
	}
}
