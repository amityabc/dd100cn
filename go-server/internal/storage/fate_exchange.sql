CREATE TABLE s9a1.fate_exchange_purchases (
    account text NOT NULL REFERENCES s9a1.accounts(account),
    event_id integer NOT NULL CHECK (event_id=4171),
    product_id bigint NOT NULL,
    period integer NOT NULL CHECK (
        (period=0 AND product_id IN (490023504,490023509,490023514,490023515))
        OR (period=1 AND product_id=490023506)
        OR (period=2 AND product_id IN (490023507,490023510))),
    period_key text NOT NULL CHECK (
        (period=0 AND period_key='') OR (period IN(1,2) AND period_key ~ '^[0-9]{8}$' AND period_key BETWEEN '00010101' AND '99991231')),
    purchased integer NOT NULL CHECK (purchased=1),
    buyer_character_id integer NOT NULL CHECK (buyer_character_id BETWEEN 1 AND 65535),
    account_revision bigint NOT NULL CHECK (account_revision>0),
    character_revision bigint NOT NULL CHECK (character_revision>0),
    receipt_key text NOT NULL CHECK (receipt_key='fate-exchange:4171:'||product_id||':'||period||':'||period_key),
    receipt_input_json text NOT NULL CHECK (jsonb_typeof(receipt_input_json::jsonb)='object' AND length(receipt_input_json) BETWEEN 1 AND 65536),
    receipt_result_json text NOT NULL CHECK (jsonb_typeof(receipt_result_json::jsonb)='object' AND length(receipt_result_json) BETWEEN 1 AND 65536),
    receipt_grants_json text NOT NULL CHECK (jsonb_array_length(receipt_grants_json::jsonb)=1 AND length(receipt_grants_json) BETWEEN 1 AND 65536),
    PRIMARY KEY(account,event_id,product_id,period,period_key),
    UNIQUE(account,account_revision),
    UNIQUE(account,receipt_key)
);
