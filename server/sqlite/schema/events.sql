CREATE INDEX run_events_page_budget
    ON run_events(run_id, sequence, payload_wire_bytes, id, event_type);

CREATE TRIGGER run_events_wire_guard_insert
BEFORE INSERT ON run_events
WHEN NEW.payload_wire IS NULL
  OR NEW.event_wire_prefix IS NULL
  OR NEW.event_wire_suffix IS NULL
  OR NOT json_valid(NEW.payload_wire)
  OR NEW.payload_wire <> NEW.payload
  OR NEW.payload_wire_bytes <> length(CAST(NEW.payload_wire AS BLOB))
  OR NEW.payload_wire_bytes < 2
  OR NEW.payload_wire_bytes > 8323072
  OR NEW.event_wire_bytes <> length(CAST(NEW.event_wire_prefix AS BLOB))
        + NEW.payload_wire_bytes + length(CAST(NEW.event_wire_suffix AS BLOB))
  OR NEW.event_wire_bytes > 8388608
  OR NEW.occurred_at NOT GLOB '????-??-??T??:??:??.?????????Z'
  OR CASE
      WHEN NOT json_valid(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix) THEN 1
      ELSE json_type(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix) <> 'object'
        OR (SELECT count(*) FROM json_each(
            NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix
        )) <> 7
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.id') IS NOT NEW.id
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.run_id') IS NOT NEW.run_id
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.lease_epoch') IS NOT NEW.lease_epoch
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.sequence') IS NOT NEW.sequence
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.type') IS NOT NEW.event_type
        OR COALESCE(json(json_extract(
            NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.payload'
        )), 'null') <> json(NEW.payload_wire)
        OR json_extract(
            NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.occurred_at'
        ) IS NOT rtrim(rtrim(substr(NEW.occurred_at, 1, length(NEW.occurred_at) - 1), '0'), '.') || 'Z'
     END
BEGIN
    SELECT RAISE(ABORT, 'run_event_wire_size_invalid');
END;

CREATE TRIGGER run_events_wire_guard_update
BEFORE UPDATE OF id, run_id, lease_epoch, sequence, event_type, payload,
    payload_wire, event_wire_prefix, event_wire_suffix, payload_wire_bytes,
    event_wire_bytes, occurred_at
ON run_events
WHEN NEW.payload_wire IS NULL
  OR NEW.event_wire_prefix IS NULL
  OR NEW.event_wire_suffix IS NULL
  OR NOT json_valid(NEW.payload_wire)
  OR NEW.payload_wire <> NEW.payload
  OR NEW.payload_wire_bytes <> length(CAST(NEW.payload_wire AS BLOB))
  OR NEW.payload_wire_bytes < 2
  OR NEW.payload_wire_bytes > 8323072
  OR NEW.event_wire_bytes <> length(CAST(NEW.event_wire_prefix AS BLOB))
        + NEW.payload_wire_bytes + length(CAST(NEW.event_wire_suffix AS BLOB))
  OR NEW.event_wire_bytes > 8388608
  OR NEW.occurred_at NOT GLOB '????-??-??T??:??:??.?????????Z'
  OR CASE
      WHEN NOT json_valid(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix) THEN 1
      ELSE json_type(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix) <> 'object'
        OR (SELECT count(*) FROM json_each(
            NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix
        )) <> 7
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.id') IS NOT NEW.id
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.run_id') IS NOT NEW.run_id
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.lease_epoch') IS NOT NEW.lease_epoch
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.sequence') IS NOT NEW.sequence
        OR json_extract(NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.type') IS NOT NEW.event_type
        OR COALESCE(json(json_extract(
            NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.payload'
        )), 'null') <> json(NEW.payload_wire)
        OR json_extract(
            NEW.event_wire_prefix || NEW.payload_wire || NEW.event_wire_suffix, '$.occurred_at'
        ) IS NOT rtrim(rtrim(substr(NEW.occurred_at, 1, length(NEW.occurred_at) - 1), '0'), '.') || 'Z'
     END
BEGIN
    SELECT RAISE(ABORT, 'run_event_wire_size_invalid');
END;
