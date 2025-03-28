BEGIN;

CREATE TABLE states (
    "id"               TEXT    NOT NULL,
    "created"          BIGINT  NOT NULL,
    "domain_name"      TEXT    NOT NULL,
    "schema"           TEXT,
    "contract_address" TEXT,
    "data"             TEXT,
    PRIMARY KEY ("domain_name", "id"),
    FOREIGN KEY ("domain_name", "schema") REFERENCES schemas ("domain_name", "id") ON DELETE CASCADE
);
CREATE INDEX states_by_domain ON states("domain_name", "schema", "contract_address");

CREATE TABLE state_labels (
    "domain_name" TEXT    NOT NULL,
    "state"       TEXT    NOT NULL,
    "label"       TEXT    NOT NULL,
    "value"       TEXT,
    PRIMARY KEY ("domain_name", "state", "label"),
    FOREIGN KEY ("domain_name", "state") REFERENCES states ("domain_name", "id") ON DELETE CASCADE
);
CREATE INDEX state_labels_value ON state_labels("value");

CREATE TABLE state_int64_labels (
    "domain_name" TEXT    NOT NULL,
    "state"       TEXT    NOT NULL,
    "label"       TEXT    NOT NULL,
    "value"       BIGINT,
    PRIMARY KEY ("domain_name", "state", "label"),
    FOREIGN KEY ("domain_name", "state")  REFERENCES states ("domain_name", "id") ON DELETE CASCADE
);
CREATE INDEX state_int64_labels_value ON state_int64_labels("value");

COMMIT;