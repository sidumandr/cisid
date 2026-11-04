-- Files a job's build produced, uploaded by its agent. The content is kept
-- in chunks, so servers sharing the database serve every artifact without
-- shared storage. An artifact counts once complete; an upload that broke
-- off leaves an incomplete row that the server deletes.
CREATE TABLE artifacts (
    id         BIGSERIAL   PRIMARY KEY,
    job_id     BIGINT      NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    name       TEXT        NOT NULL,
    size       BIGINT      NOT NULL DEFAULT 0,
    sha256     TEXT        NOT NULL DEFAULT '',
    complete   BOOLEAN     NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (job_id, name)
);

CREATE TABLE artifact_chunks (
    artifact_id BIGINT NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    n           INT    NOT NULL,
    data        BYTEA  NOT NULL,
    PRIMARY KEY (artifact_id, n)
);
