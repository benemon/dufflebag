CREATE OR REPLACE FUNCTION reject_channel_assignment_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'channel assignment history is append-only';
END
$$;
