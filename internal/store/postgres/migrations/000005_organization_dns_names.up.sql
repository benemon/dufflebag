DO $$
DECLARE
    invalid_names text;
BEGIN
    SELECT string_agg(quote_literal(name), ', ' ORDER BY name)
    INTO invalid_names
    FROM public.organizations
    WHERE name !~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$';

    IF invalid_names IS NOT NULL THEN
        RAISE EXCEPTION 'organization names must be lowercase RFC 1123 DNS labels: 1 to 63 characters using lowercase letters, digits, and hyphens, with no leading or trailing hyphen; offending names: %', invalid_names;
    END IF;
END
$$;

ALTER TABLE public.organizations
    DROP CONSTRAINT organizations_name_check;

ALTER TABLE public.organizations
    ADD CONSTRAINT organizations_name_check
    CHECK ((name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'));
