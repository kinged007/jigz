-- Anonymised fixture of a sortie workflow DB at schema_migrations version 18
-- (engine 1.24.0+pi.github-pr.1 era). Schema copied from real DBs.
-- Load: sqlite3 fixture.db < schema18.sql
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TEXT    NOT NULL
);
CREATE TABLE run_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id      TEXT    NOT NULL,
    identifier    TEXT    NOT NULL,
    attempt       INTEGER NOT NULL,
    agent_adapter TEXT    NOT NULL,
    workspace     TEXT    NOT NULL,
    started_at    TEXT    NOT NULL,
    completed_at  TEXT    NOT NULL,
    status        TEXT    NOT NULL,
    error         TEXT,
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
    tokens_measured INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX idx_run_history_issue_id ON run_history(issue_id);
CREATE TABLE session_metadata (
    issue_id      TEXT    PRIMARY KEY,
    session_id    TEXT    NOT NULL,
    agent_pid     TEXT,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens  INTEGER NOT NULL DEFAULT 0,
    updated_at    TEXT    NOT NULL,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    model_name TEXT NOT NULL DEFAULT '',
    api_request_count INTEGER NOT NULL DEFAULT 0,
    api_requests_measured INTEGER NOT NULL DEFAULT 0,
    dispatch_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE aggregate_metrics (
    key             TEXT    PRIMARY KEY,
    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    total_tokens    INTEGER NOT NULL DEFAULT 0,
    seconds_running REAL    NOT NULL DEFAULT 0,
    updated_at      TEXT    NOT NULL,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    unmeasured_sessions INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE budget_hold_notices (
    issue_id   TEXT PRIMARY KEY,
    reason     TEXT NOT NULL,
    noticed_at TEXT NOT NULL
);
CREATE TABLE handoff_absence_resets (
    issue_id     TEXT    PRIMARY KEY,
    reset_run_id INTEGER NOT NULL,
    updated_at   TEXT    NOT NULL
);
CREATE TABLE parked_issues (
    issue_id      TEXT    PRIMARY KEY,
    identifier    TEXT    NOT NULL,
    display_id    TEXT,
    reason        TEXT    NOT NULL,
    parked_state  TEXT    NOT NULL DEFAULT '',
    label         TEXT    NOT NULL,
    label_applied INTEGER NOT NULL DEFAULT 0,
    parked_at     TEXT    NOT NULL
);
CREATE TABLE reaction_fingerprints (
    issue_id    TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    fingerprint TEXT    NOT NULL,
    dispatched  INTEGER NOT NULL DEFAULT 0,
    updated_at  TEXT    NOT NULL,
    PRIMARY KEY (issue_id, kind)
);
CREATE TABLE retry_entries (
    issue_id   TEXT    PRIMARY KEY,
    identifier TEXT    NOT NULL,
    attempt    INTEGER NOT NULL,
    due_at_ms  INTEGER NOT NULL,
    error      TEXT,
    session_id TEXT,
    rule_name TEXT NOT NULL DEFAULT '',
    template_id TEXT NOT NULL DEFAULT '',
    agent_kind TEXT NOT NULL DEFAULT ''
);

INSERT INTO schema_migrations(version, applied_at)
  VALUES (1,'2026-10-01T10:00:00Z'),(2,'2026-10-01T10:00:00Z'),(3,'2026-10-01T10:00:00Z'),
  (4,'2026-10-01T10:00:00Z'),(5,'2026-10-01T10:00:00Z'),(6,'2026-10-01T10:00:00Z'),
  (7,'2026-10-01T10:00:00Z'),(8,'2026-10-01T10:00:00Z'),(9,'2026-10-01T10:00:00Z'),
  (10,'2026-10-01T10:00:00Z'),(11,'2026-10-01T10:00:00Z'),(12,'2026-10-01T10:00:00Z'),
  (13,'2026-10-01T10:00:00Z'),(14,'2026-10-01T10:00:00Z'),(15,'2026-10-01T10:00:00Z'),
  (16,'2026-10-01T10:00:00Z'),(17,'2026-10-01T10:00:00Z'),(18,'2026-10-01T10:00:00Z');

-- 16 runs in a build workflow. Issues: 10 (clean success), 11 (4 failed
-- workspace attempts then success), 12 (cancelled, unmeasured), 13 (failed
-- handoff withheld), 14 (failed twice then success), 15 (cancelled mid-run
-- with tokens measured), 16 (succeeded, unmeasured).
INSERT INTO run_history
 (id,issue_id,identifier,attempt,agent_adapter,workspace,started_at,completed_at,status,error,workflow_file,turns_completed,display_identifier,review_metadata,rule_name,template_id,input_tokens,output_tokens,total_tokens,cache_read_tokens,tokens_measured)
VALUES
 (1,'10','10',1,'claude-code','/work/acme/app/.sortie/workspaces/build/10','2026-10-12T15:02:02Z','2026-10-12T15:05:37Z','succeeded',NULL,'WORKFLOW.build.md',1,'acme/app#10',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',763270,5059,768329,722339,1),
 (2,'11','11',1,'claude-code','','2026-10-13T14:41:37Z','2026-10-13T14:41:40Z','failed','workspace preparation: hook run: exit_code=1: exit status 1','WORKFLOW.build.md',0,'acme/app#11',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',0,0,0,0,1),
 (3,'11','11',2,'claude-code','','2026-10-13T14:41:51Z','2026-10-13T14:41:53Z','failed','workspace preparation: hook run: exit_code=1: exit status 1','WORKFLOW.build.md',0,'acme/app#11',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',0,0,0,0,1),
 (4,'11','11',3,'claude-code','','2026-10-13T14:42:14Z','2026-10-13T14:42:16Z','failed','workspace preparation: hook run: exit_code=1: exit status 1','WORKFLOW.build.md',0,'acme/app#11',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',0,0,0,0,1),
 (5,'11','11',4,'claude-code','/work/acme/app/.sortie/workspaces/build/11','2026-10-13T14:44:22Z','2026-10-13T14:49:25Z','succeeded',NULL,'WORKFLOW.build.md',1,'acme/app#11',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',439421,2669,442090,404069,1),
 (6,'12','12',1,'pi','','2026-10-13T15:16:29Z','2026-10-13T15:16:29Z','cancelled','workspace preparation: context canceled','WORKFLOW.build.md',0,'acme/app#12',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',0,0,0,0,1),
 (7,'12','12',1,'pi','/work/acme/app/.sortie/workspaces/build/12','2026-10-13T15:18:05Z','2026-10-13T15:25:21Z','cancelled','agent turn 1: agent: turn_cancelled: turn cancelled','WORKFLOW.build.md',0,'acme/app#12',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',0,0,0,0,0),
 (8,'13','13',1,'codex','/work/acme/app/.sortie/workspaces/build/13','2026-10-13T16:00:00Z','2026-10-13T16:04:10Z','failed','handoff withheld: absence of work observed under observed policy','WORKFLOW.build.md',2,'acme/app#13',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',120000,3000,123000,90000,1),
 (9,'14','14',1,'claude-code','/work/acme/app/.sortie/workspaces/build/14','2026-10-13T16:10:00Z','2026-10-13T16:10:40Z','failed','agent session start: agent: response_error: handshake failed: initialize: context deadline exceeded','WORKFLOW.build.md',0,'acme/app#14',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',0,0,0,0,1),
 (10,'14','14',2,'claude-code','/work/acme/app/.sortie/workspaces/build/14','2026-10-13T16:11:30Z','2026-10-13T16:12:10Z','failed','agent session start: agent: response_error: handshake failed: initialize: context deadline exceeded','WORKFLOW.build.md',0,'acme/app#14',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',0,0,0,0,1),
 (11,'14','14',3,'claude-code','/work/acme/app/.sortie/workspaces/build/14','2026-10-13T16:14:00Z','2026-10-13T16:20:30Z','succeeded',NULL,'WORKFLOW.build.md',10,'acme/app#14',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',3163265,28860,3192125,2917187,1),
 (12,'15','15',1,'claude-code','/work/acme/app/.sortie/workspaces/build/15','2026-10-13T17:00:00Z','2026-10-13T17:02:03Z','cancelled','agent turn 4: agent: turn_cancelled: turn cancelled: context canceled','WORKFLOW.build.md',3,'acme/app#15',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',216000,2100,218100,200000,1),
 (13,'16','16',1,'pi','/work/acme/app/.sortie/workspaces/build/16','2026-09-27T20:13:31Z','2026-09-27T20:15:34Z','succeeded',NULL,'WORKFLOW.build.md',1,'acme/other#16',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',0,0,0,0,0),
 (14,'17','17',1,'claude-code','/work/acme/app/.sortie/workspaces/build/17','2026-10-14T09:00:00Z','2026-10-14T09:05:00Z','succeeded',NULL,'WORKFLOW.build.md',1,'acme/app#17',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',500000,4000,504000,450000,1),
 (15,'18','18',1,'claude-code','','2026-10-14T09:10:00Z','2026-10-14T09:10:03Z','failed','workspace preparation: hook run: exit_code=1: exit status 1','WORKFLOW.build.md',0,'acme/app#18',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',0,0,0,0,1),
 (16,'19','19',1,'claude-code','/work/acme/app/.sortie/workspaces/build/19','2026-10-14T09:20:00Z','2026-10-14T09:22:00Z','succeeded',NULL,'WORKFLOW.build.md',1,'acme/app#19',NULL,'default','/work/acme/app/.sortie/workflows/prompts/build.md',300000,2500,302500,280000,1);

INSERT INTO session_metadata
 (issue_id,session_id,agent_pid,input_tokens,output_tokens,total_tokens,updated_at,cache_read_tokens,model_name,api_request_count,api_requests_measured,dispatch_id)
VALUES
 ('10','sess-0001','1001',763270,5059,768329,'2026-10-12T15:05:37Z',722339,'claude-sonnet-5-5',8,1,''),
 ('11','sess-0002','1002',439421,2669,442090,'2026-10-13T14:49:25Z',404069,'claude-sonnet-5-5',5,1,''),
 ('12','sess-0003','1003',0,0,0,'2026-10-13T15:25:21Z',0,'',0,0,''),
 ('16','sess-0004','1004',0,0,0,'2026-09-27T20:15:34Z',0,'coding-manual',0,0,'');

INSERT INTO aggregate_metrics
 (key,input_tokens,output_tokens,total_tokens,seconds_running,updated_at,cache_read_tokens,unmeasured_sessions)
VALUES ('agent_totals',5901956,48288,5950244,1218.5,'2026-10-14T09:22:00Z',5063595,2);

INSERT INTO handoff_absence_resets(issue_id,reset_run_id,updated_at) VALUES ('13',8,'2026-10-13T16:04:10Z');
INSERT INTO parked_issues(issue_id,identifier,display_id,reason,parked_state,label,label_applied,parked_at)
  VALUES ('13','13','acme/app#13','handoff absence','in-progress','needs-human',1,'2026-10-13T16:04:12Z');
INSERT INTO retry_entries(issue_id,identifier,attempt,due_at_ms,error,session_id,rule_name,template_id,agent_kind)
  VALUES ('18','18',2,1791969000000,'workspace preparation: hook run: exit_code=1: exit status 1',NULL,'default','','claude-code');
INSERT INTO budget_hold_notices(issue_id,reason,noticed_at) VALUES ('20','period token cap reached','2026-10-14T09:30:00Z');
INSERT INTO reaction_fingerprints(issue_id,kind,fingerprint,dispatched,updated_at) VALUES ('10','review','abc123',1,'2026-10-12T15:06:00Z');
