-- Schedules that restart or stop without a warning of their own were saved with the English
-- text of their action, which the settings now hold: they warn with the text of the settings.
UPDATE tasks SET settings = json_set(CAST(settings AS TEXT), '$.message', '')
WHERE kind = 'policy' AND json_extract(CAST(settings AS TEXT), '$.message') = CASE json_extract(CAST(settings AS TEXT), '$.action')
    WHEN 'restart' THEN 'The server restarts in {minutes} min.'
    WHEN 'stop' THEN 'The server stops in {minutes} min.'
END;
