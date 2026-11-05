CREATE TRIGGER command_outbox_clear_inline_attachments_after_completion
AFTER UPDATE OF status ON command_outbox
WHEN NEW.status IN ('completed', 'failed', 'expired')
 AND NEW.command_name IN ('run.start', 'run.steer')
BEGIN
    UPDATE command_outbox
    SET payload = json_remove(NEW.payload, '$.attachments')
    WHERE id = NEW.id;
END;
