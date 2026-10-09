ALTER TABLE public.organizations
    DROP CONSTRAINT organizations_name_check;

ALTER TABLE public.organizations
    ADD CONSTRAINT organizations_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 200)));
