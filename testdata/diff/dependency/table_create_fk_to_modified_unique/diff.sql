CREATE TABLE IF NOT EXISTS child (
    id integer,
    parent_external_id integer NOT NULL,
    CONSTRAINT child_pkey PRIMARY KEY (id)
);

ALTER TABLE parent
ADD CONSTRAINT parent_external_id_key UNIQUE (external_id);

ALTER TABLE child
ADD CONSTRAINT child_parent_external_id_fkey FOREIGN KEY (parent_external_id) REFERENCES parent (external_id);
