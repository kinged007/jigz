-- Schema of the database an engine run creates.
-- Recorded with: make schema-fixture ENGINE=<repo>@<tag>
CREATE TABLE aggregate_metrics (
    key TEXT,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    seconds_running REAL NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    unmeasured_sessions INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE attention_acknowledgements (
    item_id TEXT,
    acknowledged_at TEXT NOT NULL
);
CREATE TABLE budget_hold_notices (
    issue_id TEXT,
    reason TEXT NOT NULL,
    noticed_at TEXT NOT NULL
);
CREATE TABLE handoff_absence_resets (
    issue_id TEXT,
    reset_run_id INTEGER NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE issue_outcomes (
    issue_id TEXT,
    kind TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT '',
    merge_reaction_kind TEXT NOT NULL DEFAULT '',
    pr_number INTEGER NOT NULL DEFAULT 0,
    pr_repository TEXT NOT NULL DEFAULT ''
);
CREATE TABLE live_passes (
    issue_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    chain_id TEXT NOT NULL,
    rule_name TEXT NOT NULL,
    stage_previous TEXT NOT NULL,
    PRIMARY KEY (issue_id, kind)
);
CREATE TABLE parked_issues (
    issue_id TEXT,
    identifier TEXT NOT NULL,
    display_id TEXT,
    reason TEXT NOT NULL,
    parked_state TEXT NOT NULL DEFAULT '',
    label TEXT NOT NULL,
    label_applied INTEGER NOT NULL DEFAULT 0,
    parked_at TEXT NOT NULL,
    issue_title TEXT NOT NULL DEFAULT '',
    issue_url TEXT NOT NULL DEFAULT '',
    rule_name TEXT NOT NULL DEFAULT '',
    agent_kind TEXT NOT NULL DEFAULT '',
    configured_model TEXT NOT NULL DEFAULT '',
    stage_previous TEXT NOT NULL DEFAULT '',
    stage_previous_outcome TEXT NOT NULL DEFAULT '',
    chain_id TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT NOT NULL DEFAULT '',
    issue_type TEXT NOT NULL DEFAULT '',
    issue_labels TEXT NOT NULL DEFAULT ''
);
CREATE TABLE reaction_activity (
    id INTEGER PRIMARY KEY,
    issue_id TEXT NOT NULL,
    chain_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    activity TEXT NOT NULL,
    recorded_at TEXT NOT NULL
);
CREATE TABLE reaction_escalations (
    issue_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    escalated_at TEXT,
    escalation_trigger TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    pr_number INTEGER NOT NULL,
    pr_owner TEXT NOT NULL,
    pr_repo TEXT NOT NULL,
    pr_branch TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (issue_id, kind)
);
CREATE TABLE reaction_fingerprints (
    issue_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    dispatched INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (issue_id, kind)
);
CREATE TABLE reaction_handoffs (
    issue_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    comment_id TEXT NOT NULL,
    PRIMARY KEY (issue_id, kind, comment_id)
);
CREATE TABLE retry_entries (
    issue_id TEXT,
    identifier TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    due_at_ms INTEGER NOT NULL,
    error TEXT,
    session_id TEXT,
    rule_name TEXT NOT NULL DEFAULT '',
    template_id TEXT NOT NULL DEFAULT '',
    agent_kind TEXT NOT NULL DEFAULT '',
    stage_previous TEXT NOT NULL DEFAULT '',
    stage_previous_outcome TEXT NOT NULL DEFAULT '',
    chain_id TEXT NOT NULL DEFAULT '',
    display_identifier TEXT NOT NULL DEFAULT '',
    issue_title TEXT NOT NULL DEFAULT '',
    issue_url TEXT NOT NULL DEFAULT '',
    trigger TEXT NOT NULL DEFAULT '',
    configured_model TEXT NOT NULL DEFAULT '',
    issue_type TEXT NOT NULL DEFAULT '',
    issue_labels TEXT NOT NULL DEFAULT '',
    issue_state TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT NOT NULL DEFAULT '',
    origin_trigger TEXT NOT NULL DEFAULT ''
);
CREATE TABLE run_events (
    dispatch_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    at TEXT NOT NULL,
    event TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    size INTEGER NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (dispatch_id, seq)
);
CREATE TABLE run_events_total (
    id INTEGER PRIMARY KEY,
    bytes INTEGER NOT NULL
);
CREATE TABLE run_history (
    id INTEGER PRIMARY KEY,
    issue_id TEXT NOT NULL,
    identifier TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    agent_adapter TEXT NOT NULL,
    workspace TEXT NOT NULL,
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    status TEXT NOT NULL,
    error TEXT,
    workflow_file TEXT,
    turns_completed INTEGER NOT NULL DEFAULT 0,
    display_identifier TEXT,
    review_metadata TEXT,
    rule_name TEXT NOT NULL DEFAULT '',
    template_id TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    tokens_measured INTEGER NOT NULL DEFAULT 1,
    unaccounted_turns INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    configured_model TEXT NOT NULL DEFAULT '',
    configured_effort TEXT NOT NULL DEFAULT '',
    reported_model TEXT NOT NULL DEFAULT '',
    stage_previous TEXT NOT NULL DEFAULT '',
    stage_previous_outcome TEXT NOT NULL DEFAULT '',
    chain_id TEXT NOT NULL DEFAULT '',
    stage_target TEXT NOT NULL DEFAULT '',
    stage_result TEXT NOT NULL DEFAULT '',
    issue_title TEXT NOT NULL DEFAULT '',
    issue_url TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT NOT NULL DEFAULT '',
    trigger TEXT,
    reaction_kind TEXT,
    session_id TEXT,
    ssh_host TEXT,
    first_after_hop INTEGER NOT NULL DEFAULT 0,
    dispatch_id TEXT,
    agent_blocked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);
CREATE TABLE session_metadata (
    issue_id TEXT,
    session_id TEXT NOT NULL,
    agent_pid TEXT,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    model_name TEXT NOT NULL DEFAULT '',
    api_request_count INTEGER NOT NULL DEFAULT 0,
    api_requests_measured INTEGER NOT NULL DEFAULT 0,
    dispatch_id TEXT NOT NULL DEFAULT '',
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    unaccounted_turns INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE stage_hops (
    issue_id TEXT,
    identifier TEXT NOT NULL,
    hop_count INTEGER NOT NULL,
    source_rule TEXT NOT NULL,
    target_rule TEXT NOT NULL,
    target_label TEXT NOT NULL,
    previous_outcome TEXT NOT NULL,
    source_dispatch_id TEXT NOT NULL,
    target_observed INTEGER NOT NULL DEFAULT 0,
    hopped_at TEXT NOT NULL,
    chain_id TEXT NOT NULL DEFAULT '',
    display_identifier TEXT NOT NULL DEFAULT '',
    issue_title TEXT NOT NULL DEFAULT '',
    issue_url TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT NOT NULL DEFAULT '',
    run_dispatched INTEGER NOT NULL DEFAULT 0,
    issue_type TEXT NOT NULL DEFAULT '',
    issue_labels TEXT NOT NULL DEFAULT '',
    issue_state TEXT NOT NULL DEFAULT ''
);
INSERT INTO schema_migrations VALUES (38, '');
