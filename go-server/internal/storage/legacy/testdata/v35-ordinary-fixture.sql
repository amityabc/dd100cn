-- Apply to a disposable copy of the real one-character C5 baseline.
-- Weapon inputs are the independent LocalWeaponTests.Grant test vector.
UPDATE local_entry_characters SET level=3,town=38,area=2,revision=revision+1 WHERE id=1;
INSERT INTO local_equipped_weapons VALUES
    (1,'112233445566778899aabbccddeeff00',287454020,19088743,45,45,3,1,printf('%.*c',64,'A'));
UPDATE local_entry_accounts SET asset_revision=3,cera=51,cargo_capacity=8;
INSERT INTO local_account_premium VALUES ('local',22,2000000001),('local',27,2000000002),('local',97,2000000003),('local',84,2000000004);
UPDATE local_entry_characters SET gold=19 WHERE id=1;
UPDATE local_character_receipts SET receipts_json=
    '{"storage-fixture:1":{"Key":"storage-fixture:1","Input":"old argument","Result":"old result","Revision":1,"Grants":[],"Future":{"raw":18446744073709551615}}}'
    WHERE character_id=1;
