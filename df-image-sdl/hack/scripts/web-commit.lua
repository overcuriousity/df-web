-- web-commit: apply a batch of pending changes from /therapist.
-- Invoked via: dfhack-run web-commit '<JSON payload>'
-- Payload shape (every key optional):
--   { "labors":[{"unit_id":N,"labor":I,"enabled":bool}, ...],
--     "nicknames":[{"unit_id":N,"nickname":"..."}, ...],
--     "custom_profession":[{"unit_id":N,"name":"..."}, ...] }
-- Output: JSON {"applied":N, "errors":[{"kind":"...","unit_id":N,"reason":"..."}, ...]}.
--
-- The proxy strips ANSI escapes and forwards JSON output verbatim. Errors are
-- collected and returned rather than raised so a single bad cell doesn't
-- abort the whole commit.

local json = require('json')

local args = {...}
local payload_str = args[1] or '{}'
local ok, payload = pcall(json.decode, payload_str)
if not ok or type(payload) ~= 'table' then
    print(json.encode({ applied = 0, errors = { { kind = 'parse', reason = 'invalid JSON payload' } } }))
    return
end

local applied = 0
local errors  = {}

local function err(kind, unit_id, reason)
    errors[#errors + 1] = { kind = kind, unit_id = unit_id, reason = reason }
end

local last_labor = df.unit_labor._last_item

-- ---- labors ---------------------------------------------------------------
for _, c in ipairs(payload.labors or {}) do
    local u = df.unit.find(c.unit_id)
    if not u then
        err('labor', c.unit_id, 'unit not found')
    elseif type(c.labor) ~= 'number' or c.labor < 0 or c.labor > last_labor then
        err('labor', c.unit_id, 'labor id out of range')
    else
        u.status.labors[c.labor] = c.enabled and true or false
        applied = applied + 1
    end
end

-- ---- nicknames ------------------------------------------------------------
for _, c in ipairs(payload.nicknames or {}) do
    local u = df.unit.find(c.unit_id)
    if not u then
        err('nickname', c.unit_id, 'unit not found')
    else
        local nick = dfhack.utf2df(c.nickname or '')
        -- Apply through DFHack so historical-figure and identity copies stay in sync.
        local ok_set = pcall(function() dfhack.units.setNickname(u, nick) end)
        if not ok_set then
            -- Fallback: write the unit-name field directly.
            u.name.nickname = nick
        end
        applied = applied + 1
    end
end

-- ---- custom_profession ----------------------------------------------------
for _, c in ipairs(payload.custom_profession or {}) do
    local u = df.unit.find(c.unit_id)
    if not u then
        err('custom_profession', c.unit_id, 'unit not found')
    else
        u.custom_profession = dfhack.utf2df(c.name or '')
        applied = applied + 1
    end
end

print(json.encode({ applied = applied, errors = errors }))
