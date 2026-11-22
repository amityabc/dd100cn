CREATE TABLE local_entry_accounts (
    account TEXT PRIMARY KEY CHECK(account='local'), owner TEXT NULL,
    epoch INTEGER NOT NULL DEFAULT 0 CHECK(epoch>=0), phase INTEGER NOT NULL CHECK(phase BETWEEN 0 AND 5),
    selected_id INTEGER NULL, cera INTEGER NOT NULL DEFAULT 0 CHECK(cera>=0));
CREATE TABLE local_entry_characters (
    id INTEGER PRIMARY KEY AUTOINCREMENT CHECK(id BETWEEN 1 AND 65535),
    account TEXT NOT NULL REFERENCES local_entry_accounts(account),
    storage_slot INTEGER NOT NULL CHECK(storage_slot BETWEEN 0 AND 31),
    name TEXT NOT NULL CHECK(length(name) BETWEEN 2 AND 30), name_key TEXT NOT NULL,
    job INTEGER NOT NULL CHECK(job BETWEEN 0 AND 15), level INTEGER NOT NULL CHECK(level BETWEEN 1 AND 255),
    created INTEGER NOT NULL CHECK(created BETWEEN 0 AND 4294967295), revision INTEGER NOT NULL CHECK(revision>=0),
    stats_json TEXT NOT NULL, resource_hash TEXT NOT NULL CHECK(length(resource_hash)=64 AND resource_hash NOT GLOB '*[^0-9a-fA-F]*'),
    town INTEGER NOT NULL CHECK(town BETWEEN 0 AND 65535), area INTEGER NOT NULL CHECK(area BETWEEN 0 AND 65535),
    x INTEGER NOT NULL CHECK(x BETWEEN 0 AND 65535), y INTEGER NOT NULL CHECK(y BETWEEN 0 AND 65535),
    direction INTEGER NOT NULL CHECK(direction BETWEEN 0 AND 7), state INTEGER NOT NULL CHECK(state BETWEEN 0 AND 255),
    wire_tail INTEGER NOT NULL CHECK(wire_tail BETWEEN 0 AND 65535),
    UNIQUE(account,storage_slot), UNIQUE(account,name_key));
INSERT INTO local_entry_accounts(account,owner,epoch,phase,selected_id,cera) VALUES('local',NULL,0,5,NULL,0);
ALTER TABLE local_entry_characters ADD COLUMN return_town INTEGER NULL CHECK(return_town BETWEEN 0 AND 65535);
ALTER TABLE local_entry_characters ADD COLUMN return_area INTEGER NULL CHECK(return_area BETWEEN 0 AND 65535);
ALTER TABLE local_entry_characters ADD COLUMN return_x INTEGER NULL CHECK(return_x BETWEEN 0 AND 65535);
ALTER TABLE local_entry_characters ADD COLUMN return_y INTEGER NULL CHECK(return_y BETWEEN 0 AND 65535);
ALTER TABLE local_entry_characters ADD COLUMN return_direction INTEGER NULL CHECK(return_direction BETWEEN 0 AND 7);
CREATE TABLE local_equipped_weapons (
    character_id INTEGER PRIMARY KEY REFERENCES local_entry_characters(id),
    instance_id TEXT NOT NULL UNIQUE CHECK(length(instance_id)=32 AND instance_id NOT GLOB '*[^0-9a-fA-F]*'
        AND instance_id<>'00000000000000000000000000000000'),
    template_id INTEGER NOT NULL CHECK(template_id BETWEEN 1 AND 4294967294),
    quality_seed INTEGER NOT NULL CHECK(quality_seed BETWEEN 1 AND 999999998),
    durability INTEGER NOT NULL CHECK(durability>=0 AND durability<=maximum_durability),
    maximum_durability INTEGER NOT NULL CHECK(maximum_durability BETWEEN 1 AND 65535),
    minimum_level INTEGER NOT NULL CHECK(minimum_level BETWEEN 1 AND 255),
    usable_jobs INTEGER NOT NULL CHECK(usable_jobs BETWEEN 1 AND 65535),
    resource_hash TEXT NOT NULL CHECK(length(resource_hash)=64 AND resource_hash NOT GLOB '*[^0-9a-fA-F]*'));
INSERT INTO local_entry_characters
    (id,account,storage_slot,name,name_key,job,level,created,revision,stats_json,resource_hash,
     town,area,x,y,direction,state,wire_tail,return_town,return_area,return_x,return_y,return_direction)
VALUES
    (200,'local',2,'Preserved','preserved',0,3,17,7,'{"Values":{"hp_max":103}}',
     'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
     38,0,450,234,5,3,100,38,2,500,321,5),
    (273,'local',8,'Deleted','deleted',0,3,17,7,'{"Values":{"hp_max":103}}',
     'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA',
     38,0,450,234,5,3,100,NULL,NULL,NULL,NULL,NULL);
DELETE FROM local_entry_characters WHERE id=273;
INSERT INTO local_equipped_weapons VALUES
    (200,'ffeeddccbbaa99887766554433221100',287454020,19088743,45,45,3,1,
     'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA');
