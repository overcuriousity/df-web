-- web-setlabor: set one labor on a specific dwarf unit.
-- Usage: dfhack-run web-setlabor <unit_id> <labor_id> <0|1>
-- Returns nothing on success; prints an error line on failure.

local args = {...}
if #args ~= 3 then
    error('usage: web-setlabor <unit_id> <labor_id> <0|1>')
end

local unit_id  = tonumber(args[1])
local labor_id = tonumber(args[2])
local enabled  = args[3] == '1'

if not unit_id or not labor_id or labor_id < 0 or labor_id > 119 then
    error('invalid arguments')
end

local u = df.unit.find(unit_id)
if not u then
    error('unit not found: ' .. tostring(unit_id))
end

u.status.labors[labor_id] = enabled
