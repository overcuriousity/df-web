-- web-commit: apply a batch of pending changes from /therapist.
-- Invoked via: dfhack-run web-commit <hex-encoded JSON>
-- Payload shape (every key optional):
--   { "nicknames":[{"unit_id":N,"nickname":"..."}, ...],
--     "custom_profession":[{"unit_id":N,"name":"..."}, ...] }
-- Output: JSON {"applied":N, "errors":[{"kind":"...","unit_id":N,"reason":"..."}, ...]}.
--
-- Why hex on the wire: dfhack-run's command parser splits the script's argv
-- on whitespace, so a JSON payload like {"nickname":"Test User"} arrived in
-- Lua as {"nickname":"Test instead of the whole document, and json.decode
-- failed silently — making it look like Commit did nothing in-game. Hex
-- encoding avoids whitespace, quotes, and shell metachars entirely. The 2x
-- size blowup is fine for batches with at most a few thousand changes.

local json = require('json')

local function hex_decode(s)
    return (s:gsub('..', function(cc) return string.char(tonumber(cc, 16) or 0) end))
end

local args = {...}
local hex_in = args[1] or ''
local payload_str = (#hex_in > 0) and hex_decode(hex_in) or '{}'
local ok, payload = pcall(json.decode, payload_str)
if not ok or type(payload) ~= 'table' then
    print(json.encode({ applied = 0, errors = { { kind = 'parse', reason = 'invalid JSON payload after hex-decode' } } }))
    return
end

local applied = 0
local errors  = {}

local function err(kind, unit_id, reason)
    errors[#errors + 1] = { kind = kind, unit_id = unit_id, reason = reason }
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
