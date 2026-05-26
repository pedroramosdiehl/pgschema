CREATE TABLE public.parent (
    id integer PRIMARY KEY,
    external_id integer NOT NULL,
    CONSTRAINT parent_external_id_key UNIQUE (external_id)
);

CREATE TABLE public.child (
    id integer PRIMARY KEY,
    parent_external_id integer NOT NULL,
    CONSTRAINT child_parent_external_id_fkey FOREIGN KEY (parent_external_id) REFERENCES public.parent(external_id)
);
