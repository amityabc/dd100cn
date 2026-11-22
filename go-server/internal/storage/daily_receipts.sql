CREATE INDEX daily_reward_receipts ON s9a1.reward_receipts
    (character_id,split_part(event_key,':',3))
    WHERE split_part(event_key,':',1) IN ('dungeon-special','dungeon-special-credit');
