-- MySQL 8 专用、默认只读：#62-#70 默认可靠性迁移预览。
-- 禁止用于 SQLite/PostgreSQL；禁止在未备份 channels/options 前取消 UPDATE 注释。
SELECT
  id,
  name,
  CASE WHEN NOT JSON_VALID(COALESCE(NULLIF(setting, ''), '{}'))
            OR JSON_TYPE(CAST(COALESCE(NULLIF(setting, ''), '{}') AS JSON)) <> 'OBJECT'
       THEN 1 ELSE 0 END AS invalid_setting_json,
  CASE WHEN JSON_VALID(COALESCE(NULLIF(setting, ''), '{}'))
            AND JSON_EXTRACT(COALESCE(NULLIF(setting, ''), '{}'), '$.timeout_seconds') IS NULL
       THEN 1 ELSE 0 END AS need_timeout_180,
  CASE WHEN JSON_VALID(COALESCE(NULLIF(setting, ''), '{}'))
            AND JSON_EXTRACT(COALESCE(NULLIF(setting, ''), '{}'), '$.fail_threshold') IS NULL
       THEN 1 ELSE 0 END AS need_fail_threshold_3,
  CASE WHEN COALESCE(TRIM(header_override), '') = '' THEN 1 ELSE 0 END AS can_add_fingerprint_automatically,
  CASE WHEN COALESCE(TRIM(header_override), '') <> '' THEN 1 ELSE 0 END AS needs_manual_header_merge
FROM channels
WHERE id BETWEEN 62 AND 70
ORDER BY id;

-- 当前 #62-#70 预览确认 Header 均为空后，候选部署获授权才可执行。
-- 非空 Header 的渠道不会被标记为“指纹已开启”，避免开关状态与实际 Header 不一致。
-- UPDATE channels
-- SET setting = JSON_SET(
--       COALESCE(NULLIF(setting, ''), '{}'),
--       '$.timeout_seconds',
--       COALESCE(JSON_EXTRACT(COALESCE(NULLIF(setting, ''), '{}'), '$.timeout_seconds'), 180),
--       '$.fail_threshold',
--       COALESCE(JSON_EXTRACT(COALESCE(NULLIF(setting, ''), '{}'), '$.fail_threshold'), 3),
--       '$.openai_python_fingerprint_enabled',
--       CASE WHEN COALESCE(TRIM(header_override), '') = '' THEN TRUE
--            ELSE COALESCE(JSON_EXTRACT(COALESCE(NULLIF(setting, ''), '{}'), '$.openai_python_fingerprint_enabled'), FALSE) END
--     ),
--     header_override = CASE
--       WHEN COALESCE(TRIM(header_override), '') = '' THEN '{"Accept":"application/json","User-Agent":"OpenAI/Python 2.33.0","X-Stainless-Arch":"x64","X-Stainless-Async":"false","X-Stainless-Lang":"python","X-Stainless-OS":"Linux","X-Stainless-Package-Version":"2.33.0","X-Stainless-Runtime":"CPython","X-Stainless-Runtime-Version":"3.12.4"}'
--       ELSE header_override
--     END
-- WHERE id BETWEEN 62 AND 70
--   AND JSON_VALID(COALESCE(NULLIF(setting, ''), '{}'))
--   AND JSON_TYPE(CAST(COALESCE(NULLIF(setting, ''), '{}') AS JSON)) = 'OBJECT';

-- 巡检周期候选更新（具体 option key 必须在执行前重新核实）：
-- UPDATE options SET value = '120' WHERE `key` = '<已核实的巡检周期键>' AND value = '360';
