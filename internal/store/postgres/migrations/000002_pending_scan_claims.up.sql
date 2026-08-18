ALTER TABLE public.pending_scans
    ADD COLUMN claimed_at timestamp with time zone;
