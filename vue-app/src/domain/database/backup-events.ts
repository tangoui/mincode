// backend event emitted by the Go backup scheduler for every backup run. Kept
// apart from backup-jobs.ts so the app shell can listen without loading the
// backup job validators into the entry chunk.
export const BACKUP_RUN_EVENT = "db-backup:run";
