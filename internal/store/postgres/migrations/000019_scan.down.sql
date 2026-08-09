DROP TABLE build_scan_state;
DROP TABLE scan_transcripts;
DROP TRIGGER scan_findings_immutable ON scan_findings;
DROP TRIGGER scan_runs_immutable ON scan_runs;
DROP FUNCTION reject_scan_row_change();
DROP TABLE scan_findings;
DROP TABLE scan_runs;
DROP TABLE scan_run_counters;
