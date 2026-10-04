-- 028: received_attributes, the attribute keys each project's product
-- events and measures carried per day, beside events: recorded at ingest,
-- in the same transaction as the rows, the first time a key arrives each
-- day (events 0), and counted by the daily pass: events, and each key's
-- busiest (event, measure) partition's distinct values (max_values, NULL
-- until counted). It follows the raw window: the pass
-- keeps only days whose raw rows remain. The console lists it to pick a
-- project's attribute breakdowns. Empty until the first write or pass.
CREATE TABLE received_attributes (
    project_id INTEGER NOT NULL,
    day        TEXT    NOT NULL,
    attr_key   TEXT    NOT NULL,
    events     INTEGER NOT NULL,
    max_values INTEGER,
    PRIMARY KEY (project_id, day, attr_key)
) WITHOUT ROWID;
