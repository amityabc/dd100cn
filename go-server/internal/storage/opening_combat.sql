ALTER TABLE s9a1.opening_entry
    ADD COLUMN run_id uuid,
    ADD COLUMN dungeon_resource_hash text,
    ADD COLUMN experience_hash text,
    ADD CHECK(num_nonnulls(run_id,dungeon_resource_hash,experience_hash) IN (0,3));
